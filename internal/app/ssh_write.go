// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"
)

func (app *nativeApp) changesWritable() bool {
	if app.ssh != nil {
		return app.ssh.snapshot.SSHWritable
	}
	return app.repository != nil && app.writable
}

func (app *nativeApp) runSSHWrite(label string, write func(context.Context) error, succeeded func()) {
	app.runSSHWriteTask(label, false, write, succeeded)
}

func (app *nativeApp) runSSHWriteTask(label string, cancelable bool, write func(context.Context) error, succeeded func()) {
	state, dialog := app.ssh, app.dialog
	if state == nil || !state.snapshot.SSHWritable || app.closed || app.busy ||
		app.desktop != nil && (app.desktop.checkingQuit || app.desktop.quitting) {
		return
	}
	writeContext := app.ctx
	cancel := func() {}
	if cancelable {
		writeContext, cancel = context.WithCancel(app.ctx)
		app.operationCancel = func() {
			cancel()
			app.operationCancel = nil
		}
	}
	app.run(label, func(ctx context.Context) (func(), error) {
		writeErr := write(writeContext)
		canceled := cancelable && errors.Is(writeErr, context.Canceled)
		cancel()
		snapshot, refs, refreshErr := state.repo.Snapshot(ctx)
		return func() {
			if cancelable {
				app.operationCancel = nil
			}
			if app.ssh != state {
				return
			}
			if (writeErr == nil || canceled) && dialog != nil && app.dialog == dialog {
				app.closeDialog(dialog)
			}
			if writeErr == nil && succeeded != nil {
				succeeded()
			}
			if refreshErr != nil {
				state.snapshot.SSHWritable = false
				return
			}
			state.update(snapshot, refs)
			app.fileMetrics = nativeFileMetrics{}
			clear(app.selectedFiles)
			app.refreshSSHView()
		}, errors.Join(sshWriteError(writeErr, canceled), refreshErr)
	})
}

func sshWriteError(err error, canceled bool) error {
	if canceled {
		return errors.New(locales.SshSyncCanceled())
	}
	return err
}

func (app *nativeApp) commitSSH() {
	state := app.ssh
	key := sshRepositoryKey(state.repo)
	message := app.prefs.Drafts[key]
	if !state.snapshot.SSHWritable || app.screen != "sshChanges" || app.closed || app.busy || app.dialog != nil ||
		strings.TrimSpace(message) == "" || app.fileSection(true).total == 0 ||
		slices.ContainsFunc(state.snapshot.Files, func(file gitrepo.File) bool { return file.Conflict }) {
		return
	}
	snapshot, scan := state.snapshot, app.prefs.SecretScanEnabled
	features := app.gitFeatures(state.address())
	app.runTask(locales.Committing(), true, func(ctx context.Context) (func(), error) {
		result, err := state.repo.PrepareCommit(ctx, snapshot, scan)
		if err != nil {
			return nil, err
		}
		var large []gitrepo.FileInspection
		if features.checks {
			large, err = state.repo.LargeFiles(ctx, nil, result.Token, features.threshold)
			if err != nil {
				return nil, err
			}
		}
		return func() {
			proceed := func() {
				if app.ssh != state || app.prefs.Drafts[key] != message || app.screen != "sshChanges" {
					return
				}
				app.runSSHWrite(locales.Committing(), func(ctx context.Context) error {
					return state.repo.CommitStaged(ctx, snapshot, message, result.Token)
				}, func() {
					delete(app.prefs.Drafts, key)
					if err := app.savePreferences(); err != nil {
						app.showProblemDetails(locales.CommitSavedDraftFailed(err.Error()))
					}
				})
			}
			reviewSize := func() { app.reviewSSHFiles(large, features.threshold, true, proceed) }
			if len(result.Findings) > 0 {
				app.secretScanDialog(state.address(), result, reviewSize)
			} else {
				reviewSize()
			}
		}, nil
	})
}

func (app *nativeApp) stageSSHFiles(unstage bool, paths []string) {
	state := app.ssh
	if !state.snapshot.SSHWritable || app.busy || app.closed {
		return
	}
	snapshot := state.snapshot
	proceed := func() {
		app.runSSHWrite(locales.UpdatingIndex(), func(ctx context.Context) error {
			return state.repo.StageFiles(ctx, snapshot, paths, unstage)
		}, nil)
	}
	features := app.gitFeatures(state.address())
	if unstage || !features.checks {
		proceed()
		return
	}
	app.runTask(locales.CheckingLargeFiles(), true, func(ctx context.Context) (func(), error) {
		files, err := state.repo.LargeFiles(ctx, paths, "", features.threshold)
		if err != nil {
			return nil, err
		}
		return func() { app.reviewSSHFiles(files, features.threshold, false, proceed) }, nil
	})
}

func (app *nativeApp) reviewSSHFiles(files []gitrepo.FileInspection, threshold int64, commit bool, proceed func()) {
	if len(files) == 0 {
		proceed()
		return
	}
	title, action := locales.StageReviewTitle(), locales.StageNormally()
	if commit {
		title, action = locales.CommitReviewTitle(), locales.ContinueCommit()
	}
	var details strings.Builder
	details.WriteString(locales.LargeFileReviewHint(strconv.FormatInt(threshold>>20, 10)))
	for _, file := range files {
		details.WriteString("\n" + file.Path + " · " + fileInspectionSize(file.Size))
	}
	app.confirm(title, details.String(), action, proceed)
}

func (app *nativeApp) sshSwitchDialog(name string) {
	state := app.ssh
	if state == nil || !state.snapshot.SSHWritable || app.busy || app.closed || app.dialog != nil ||
		name == state.snapshot.Branch || !slices.Contains(state.refs, "refs/heads/"+name) {
		return
	}
	snapshot := state.snapshot
	app.dialog = &nativeDialog{
		title: locales.ConfirmBranchSwitch(), subject: state.address(), detail: locales.ConfirmBranchSwitchDetail(name),
		action: locales.Checkout(), confirmation: true, submit: func(*nativeDialog) {
			app.runSSHWrite(locales.SwitchingBranch(), func(ctx context.Context) error {
				return state.repo.SwitchLocalBranch(ctx, snapshot, name)
			}, func() { state.ref = "" })
		},
	}
}

func (app *nativeApp) sshSyncDialog(push bool) {
	state := app.ssh
	if state == nil || !state.snapshot.SSHWritable || state.snapshot.Detached || state.snapshot.Unborn ||
		app.busy || app.closed || app.dialog != nil {
		return
	}
	snapshot := state.snapshot
	target, title, status := snapshot.Upstream, locales.Pull(), locales.Pulling()
	if push {
		title, status = locales.Push(), locales.Pushing()
		if target == "" {
			target = "origin/" + snapshot.Branch
		}
	} else if target == "" {
		return
	}
	source, dest := snapshot.Branch, target
	if !push {
		source, dest = dest, source
	}
	app.dialog = &nativeDialog{title: title, subject: state.address(), detail: source + " → " + dest,
		action: title, confirmation: true, submit: func(*nativeDialog) {
			app.runSSHWriteTask(status, true, func(ctx context.Context) error {
				return state.repo.SyncBranch(ctx, snapshot, push)
			}, nil)
		}}
}
