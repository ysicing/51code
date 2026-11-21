// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func (app *nativeApp) canReadReflog() bool {
	return app.repository != nil && app.screen != "library" && !app.closed && !app.busy && app.dialog == nil
}

func (app *nativeApp) lostCommitsDialog() {
	if !app.canReadReflog() {
		return
	}
	path := app.repository.snapshot.Path
	var result gitrepo.LostCommits
	loading, selected := true, -1
	list := ui.ListState{Selected: &selected}
	list.Key = func(index int) any { return result.Commits[index].Hash }
	list.Label = func(index int) string { return result.Commits[index].Subject }
	dialog := &nativeDialog{
		title: locales.FindLostCommits(), dismiss: locales.Close(), action: locales.CreateRecoveryBranch(),
		width: 700,
	}
	hasSelection := func() bool { return selected >= 0 && selected < len(result.Commits) }
	dialog.canSubmit = func() bool { return app.writable && hasSelection() }
	dialog.submit = func(_ *nativeDialog) {
		if app.dialog != dialog || !dialog.valid() {
			return
		}
		entry := result.Commits[selected]
		app.closeDialog(dialog)
		app.referenceDialog(false, locales.ReferenceCommit()+" "+entry.Subject, entry.Hash, false)
	}
	dialog.body = func(c *ui.Context) {
		if loading {
			ui.Row(c).FillWidth().Padding(16, 0).Center().Gap(8).Children(func() {
				ui.Spinner(c).Size(14, 14)
				ui.Text(c, locales.ReadingReflog())
			})
			return
		}
		if len(result.Commits) == 0 {
			if dialog.problem == "" {
				ui.Box(c).FillWidth().Padding(16, 0).Center().Children(func() {
					ui.Text(c, locales.NoLostCommits()).TextColor(c.Theme().TextMuted)
				})
			}
		} else {
			_, height := c.Size()
			listHeight := min(float32(len(result.Commits)*42), max(42, min(360, height-220)))
			ui.List(c, &list, len(result.Commits), func(index int) {
				entry := result.Commits[index]
				ui.Column(c).FillWidth().MinWidth(0).Padding(5, 10).Gap(3).Children(func() {
					ui.Row(c).Gap(8).FillWidth().AlignItems(ui.Center).Children(func() {
						ui.Text(c, shortHash(entry.Hash)).Font("monospace").FontSize(11).Tooltip(entry.Hash)
						ui.Text(c, entry.Subject).Grow(1).MinWidth(0).SingleLine().Tooltip(entry.Subject)
					})
					detail := historyDate(entry.Date) + " · " + entry.Ref + " · " + entry.Action
					metadata := ui.Text(c, detail).FontSize(11).SingleLine().Tooltip(detail)
					if selected != index {
						metadata.TextColor(c.Theme().TextMuted)
					}
				})
			}).Label(locales.FindLostCommits()).FillWidth().Height(listHeight)
			disabled := !hasSelection() || app.readTask != nil || app.repository.snapshot.Head == ""
			ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
				ui.Button(c, locales.CompareCommitWithHead()).Disabled(disabled).OnClick(func() {
					if app.dialog != dialog || !hasSelection() {
						return
					}
					entry := result.Commits[selected]
					app.closeDialog(dialog)
					app.leaveConflict(func() { app.compareCommitWithHead(entry.Hash) })
				})
			})
		}
		if result.Limited {
			ui.Text(c, locales.ReflogScanLimit(gitrepo.ReflogRecoveryLimit)).FontSize(11).TextColor(c.Theme().TextMuted)
		}
	}
	app.dialog = dialog
	app.load(locales.ReadingReflog(), nativeReadReflog, func(ctx context.Context) (func(), error) {
		repo, err := app.service.manager.Get(path)
		var commits gitrepo.LostCommits
		if err == nil {
			commits, err = repo.LostCommits(ctx)
		}
		return func() {
			if app.dialog != dialog {
				return
			}
			loading, result = false, commits
			if err != nil {
				dialog.problem = err.Error()
			}
		}, nil
	})
	request := app.readTask
	dialog.cancel = func() {
		if request != nil && app.readTask == request {
			app.cancelRead()
		}
	}
}
