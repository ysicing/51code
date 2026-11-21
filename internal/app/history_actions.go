// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func (app *nativeApp) historyCommitMenu(menu *ui.Menu, commit gitrepo.Commit, services ui.Services) {
	if menu.Item(locales.CopyWorktreeCommit()).Chosen() {
		services.WriteClipboard(commit.Hash)
	}
	if menu.Item(locales.CompareCommitWithHead()).Disabled(!app.canCompareCommit()).Chosen() {
		repository, screen := app.repository, app.screen
		app.post(func() {
			if !app.closed && app.repository == repository && app.screen == screen {
				app.compareCommitWithHead(commit.Hash)
			}
		})
	}
	menu.Separator()
	unavailable := !app.canCreateReference() || app.readTask != nil && app.readTask.kind == nativeReadHistory
	if menu.Item(locales.BranchFromCommit()).Disabled(unavailable).Chosen() {
		app.referenceDialog(false, locales.ReferenceCommit()+" "+commit.Subject, commit.Hash, false)
	}
	if menu.Item(locales.TagFromCommit()).Disabled(unavailable).Chosen() {
		app.referenceDialog(true, locales.ReferenceCommit()+" "+commit.Subject, commit.Hash, false)
	}
	menu.Separator()
	writeUnavailable := !app.canApplyHistoryCommit(commit)
	if menu.Item(locales.CherryPickCommitMenu()).Disabled(writeUnavailable).Chosen() {
		app.confirmHistoryCommit(commit, "cherry-pick")
	}
	if menu.Item(locales.RevertCommitMenu()).Disabled(writeUnavailable).Chosen() {
		app.confirmHistoryCommit(commit, "revert")
	}
}

func (app *nativeApp) canCompareCommit() bool {
	return app.repository != nil && app.repository.snapshot.Head != "" && !app.busy && app.readTask == nil && app.dialog == nil
}

func (app *nativeApp) compareCommitWithHead(hash string) {
	if !app.canCompareCommit() {
		return
	}
	app.switchWorkspace("compare")
	app.comparison.base, app.comparison.target, app.comparison.mergeBase = hash, "HEAD", false
	app.startComparison()
}

func (app *nativeApp) canApplyHistoryCommit(commit gitrepo.Commit) bool {
	if commit.Hash == "" || len(commit.Parents) > 1 || !app.canCompareCommit() || !app.writable {
		return false
	}
	snapshot := app.repository.snapshot
	return !snapshot.Detached && snapshot.Branch != "" && snapshot.Operation.Kind == "" && len(snapshot.Files) == 0
}

func (app *nativeApp) confirmHistoryCommit(commit gitrepo.Commit, kind string) {
	if !app.canApplyHistoryCommit(commit) {
		return
	}
	snapshot := app.repository.snapshot
	label := operationName(kind)
	detail := shortHash(commit.Hash) + "  " + commit.Subject + "\n" + locales.CommitActionTarget(snapshot.Branch)
	app.confirm(label, detail, label, func() {
		app.mutateFormResult(nil, locales.ProcessingOperation(label), func(ctx context.Context, path string) error {
			return app.service.ApplyCommit(ctx, path, kind, commit.Hash, snapshot.Branch, snapshot.Head)
		}, func(err error) {
			if err != nil && app.repository.snapshot.Operation.Kind == kind {
				app.openHistoryWorkingChanges()
			}
		})
	})
}
