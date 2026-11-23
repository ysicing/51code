// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

type nativeRemoteProgress struct {
	action   string
	target   string
	progress gitrepo.RemoteProgress
	failed   bool
}

func (app *nativeApp) runRemoteSync(dialog *nativeDialog, action, label, target string, operation func(context.Context, string) error) {
	if app.repository == nil || !app.writable || app.busy || app.closed || app.dialog != dialog ||
		app.desktop != nil && (app.desktop.checkingQuit || app.desktop.quitting) {
		return
	}
	state := &nativeRemoteProgress{
		action: action, target: target,
		progress: gitrepo.RemoteProgress{Phase: gitrepo.RemotePreparing, Percent: gitrepo.RemotePercentUnknown},
	}
	post := app.post
	app.mutateFormResult(dialog, label, func(ctx context.Context, path string) error {
		var last gitrepo.RemoteProgress
		var sent time.Time
		report := func(progress gitrepo.RemoteProgress) {
			now := time.Now()
			if !sent.IsZero() && (progress == last ||
				progress.Phase == last.Phase && progress.Percent != 100 && now.Sub(sent) < 100*time.Millisecond) {
				return
			}
			last, sent = progress, now
			post(func() {
				if !app.closed && app.busy && app.remoteProgress == state {
					state.progress = progress
				}
			})
		}
		err := operation(gitrepo.WithRemoteProgress(ctx, report), path)
		if err == nil {
			report(gitrepo.RemoteProgress{Phase: gitrepo.RemoteRefreshing, Percent: gitrepo.RemotePercentUnknown})
		}
		return err
	}, func(err error) {
		state.failed = err != nil
		if state.failed {
			return
		}
		ctx := app.ctx
		time.AfterFunc(3*time.Second, func() {
			if ctx.Err() != nil {
				return
			}
			post(func() {
				if !app.closed && app.remoteProgress == state {
					app.remoteProgress = nil
				}
			})
		})
	})
	app.remoteProgress = state
}

func (state *nativeRemoteProgress) phaseLabel() string {
	switch state.progress.Phase {
	case gitrepo.RemoteCompressing:
		return locales.RemoteCompressingObjects()
	case gitrepo.RemoteSending:
		return locales.RemoteWritingObjects()
	case gitrepo.RemoteReceiving:
		return locales.RemoteReceivingObjects()
	case gitrepo.RemoteResolving:
		return locales.RemoteResolvingDeltas()
	case gitrepo.RemoteConfirming:
		return locales.RemoteWaitingForConfirmation()
	case gitrepo.RemoteUpdating:
		return locales.RemoteUpdatingWorktree()
	case gitrepo.RemoteRefreshing:
		return locales.Refreshing()
	default:
		return locales.RemotePreparingObjects()
	}
}

func (app *nativeApp) remoteProgressView(c *ui.Context) {
	state := app.remoteProgress
	label := app.status
	if state.target != "" {
		if state.action == "push" {
			label = locales.RemotePushingTo(state.target)
		} else {
			label = locales.RemotePullingFrom(state.target)
		}
	}
	phase := state.phaseLabel()
	if state.progress.Percent < 0 {
		ui.Spinner(c).Size(14, 14).Shrink(0)
	}
	text := label + " · " + phase
	ui.Text(c, text).FontSize(12).TextColor(c.Theme().TextMuted).MaxLines(1).MinWidth(0).Shrink(1).Tooltip(text)
	if state.progress.Percent >= 0 {
		ui.Progress(c, float64(state.progress.Percent)/100).Label(phase).Width(96).MinWidth(32).Height(4).Shrink(1)
		ui.Text(c, fmt.Sprintf("%d%%", state.progress.Percent)).FontSize(12).Shrink(0)
	}
}

func (state *nativeRemoteProgress) successLabel() string {
	if state.action == "push" {
		return locales.RemotePushComplete()
	}
	return locales.RemotePullComplete()
}
