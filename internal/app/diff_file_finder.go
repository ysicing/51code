// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"strings"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func (app *nativeApp) diffFileFinderButton(c *ui.Context) {
	label := locales.GoToChangedFile()
	iconButton(c, label, nativeIcons["search"]).Shrink(0).Disabled(!app.hasDiffFiles()).
		OnClick(app.openDiffFileFinder)
}

func (app *nativeApp) openDiffFileFinder() {
	if !app.hasDiffFiles() {
		return
	}
	entries := app.orderedDiffFiles()
	working, _, _ := app.diffFileContext()
	repository, ssh, screen := app.repository, app.ssh, app.screen
	dialog := &nativeDialog{
		title: locales.GoToChangedFile(), width: 540, height: min(340, 82+30*float32(len(entries))), closeLabel: locales.Close(),
	}
	dialog.onClose = func() { app.dialog = nil }
	query, previousQuery := "", ""
	results, selected := entries, 0
	list := ui.ListState{Selected: &selected}
	choose := func(index int) {
		if app.dialog != dialog || app.screen != screen || app.repository != repository || app.ssh != ssh ||
			index < 0 || index >= len(results) {
			return
		}
		app.closeDialog(dialog)
		app.openDiffFile(results[index], 0)
	}
	dialog.body = func(c *ui.Context) {
		if query != previousQuery {
			previousQuery = query
			results = filterDiffFiles(entries, query)
			list.Choose(0)
			list.ScrollTo(0, ui.Start)
		}
		move := func(direction int) {
			if len(results) > 0 {
				index := max(0, min(selected+direction, len(results)-1))
				list.Choose(index)
				list.ScrollIntoView(index)
			}
		}
		label := locales.FileFilter()
		ui.TextInput(c, &query).Label(label).Placeholder(label).Search().AutoFocus().FillWidth().
			OnShortcut(0, ui.KeyDown, func() { move(1) }).
			OnShortcut(0, ui.KeyUp, func() { move(-1) }).
			OnSubmit(func() { choose(selected) })
		if len(results) == 0 {
			ui.Text(c, locales.NoMatchingFiles()).FillWidth().Padding(8, 2).TextColor(c.Theme().TextMuted)
			return
		}
		list.Key = func(index int) any { return results[index] }
		list.Label = func(index int) string {
			file := results[index]
			if working {
				return file.path + " · " + diffFileGroup(file.staged)
			}
			return file.path
		}
		theme := *c.Theme()
		theme.Accent = theme.Accent.Alpha(0.16)
		theme.InactiveAccent, theme.AccentText = theme.Accent, theme.Text
		ui.Themed(c, &theme).FillWidth().Grow(1).MinHeight(0).Children(func() {
			if ui.List(c, &list, len(results), func(index int) {
				file := results[index]
				row := ui.Row(c).FillWidth().Height(30).Padding(0, 6).Gap(8).AlignItems(ui.Center).Tooltip(file.path)
				row.Children(func() {
					nativeFileIcon(c, file.path)
					ui.Text(c, file.path).FontSize(12).SingleLine().Grow(1).MinWidth(0)
					if working {
						ui.Text(c, diffFileGroup(file.staged)).FontSize(11).TextColor(c.Theme().TextMuted).Shrink(0)
					}
				})
				if row.RowClicked() {
					choose(index)
				}
			}).FillWidth().Grow(1).MinHeight(0).Label(locales.GoToChangedFile()).Submitted() {
				choose(selected)
			}
		})
	}
	app.dialog = dialog
}

func diffFileGroup(staged bool) string {
	if staged {
		return locales.StagedFiles()
	}
	return locales.UnstagedFiles()
}

func filterDiffFiles(entries []nativeDiffFile, query string) []nativeDiffFile {
	tokens := strings.Fields(strings.ToLower(query))
	if len(tokens) == 0 {
		return entries
	}
	var matches []nativeDiffFile
	for _, file := range entries {
		path := strings.ToLower(file.path)
		matchesAll := true
		for _, token := range tokens {
			if !strings.Contains(path, token) {
				matchesAll = false
				break
			}
		}
		if matchesAll {
			matches = append(matches, file)
		}
	}
	return matches
}
