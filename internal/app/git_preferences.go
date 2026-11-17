// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func (app *nativeApp) historyDateLabel() string {
	if app.prefs.HistoryDate == "committer" {
		return locales.CommitterDate()
	}
	return locales.AuthorDate()
}

func (app *nativeApp) commitDate(commit gitrepo.Commit) string {
	if app.prefs.HistoryDate == "committer" && commit.CommitterDate != "" {
		return commit.CommitterDate
	}
	return commit.Date
}

func (app *nativeApp) gitPreferences(c *ui.Context) {
	previous := *app.prefs
	settingsSection(c, locales.OperationsAndDisplay(), func() {
		settingsRow(c, locales.FileViewMode(), func() {
			app.wrapPreference(c)
		})
		settingsRow(c, locales.DoubleClickFile(), func() {
			ui.Checkbox(c, &app.prefs.DoubleClickStage, locales.StageOrUnstage()).Label(locales.DoubleClickStage()).OnClick(func() {
				app.saveBehaviorPreferences(previous)
			})
		})
		settingsRow(c, locales.HistoryDate(), func() {
			ui.MenuButton(c, app.historyDateLabel(), func(menu *ui.Menu) {
				for _, choice := range []struct{ value, label string }{
					{"author", locales.AuthorDate()}, {"committer", locales.CommitterDate()},
				} {
					if menu.Item(choice.label).Checked(app.prefs.HistoryDate == choice.value).Chosen() {
						previous := *app.prefs
						app.prefs.HistoryDate = choice.value
						app.saveBehaviorPreferences(previous)
					}
				}
			}).Label(locales.HistoryDate()).Width(210).Justify(ui.SpaceBetween)
		})
	})
	if app.settingsError != "" {
		app.inlineProblem(c, app.settingsError)
	}
}

func (app *nativeApp) wrapPreference(c *ui.Context) {
	previous := *app.prefs
	ui.Checkbox(c, &app.prefs.Wrap, locales.Wrap()).Shrink(0).OnClick(func() {
		app.saveBehaviorPreferences(previous)
		if app.settingsError != "" {
			app.status = app.settingsError
		}
	})
}

func (app *nativeApp) stageDoubleClickedFile(file gitrepo.File, staged bool) {
	if !app.prefs.DoubleClickStage || app.busy || app.closed || !app.writable || app.dialog != nil ||
		file.Conflict || app.conflictDirty() || app.repository == nil ||
		!(app.screen == "changes" || app.screen == "history" && app.historyWorking) ||
		app.readTask != nil && app.readTask.kind != nativeReadDiff {
		return
	}
	// 刷新按操作后的分组恢复同一文件，避免文件移出原分组后丢失选择。
	app.selectedFile, app.staged = file.Path, !staged
	app.stagePaths(staged, []string{file.Path})
}
