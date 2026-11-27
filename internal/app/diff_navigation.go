// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"slices"
	"sort"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

type nativeDiffFile struct {
	path   string
	staged bool
}

type nativeDiffBoundary struct {
	direction int
	cursor    editor.Pos
	side      editor.DiffSide
	layout    editor.DiffLayout
}

func (app *nativeApp) diffFileContext() (working bool, files []gitrepo.CommitFile, current nativeDiffFile) {
	switch app.screen {
	case "changes", "sshChanges":
		working = true
	case "history", "sshHistory":
		working = app.historyWorking
		files, current.path = app.commitFiles, app.commitFile
	case "search":
		if app.searchMode == nativeSearchCommits {
			files, current.path = app.commitFiles, app.commitFile
		}
	case "compare":
		if app.comparison != nil && app.comparison.result != nil {
			files, current.path = app.comparison.result.Files, app.comparison.file
		}
	}
	if working {
		current = nativeDiffFile{app.selectedFile, app.staged}
	}
	return
}

func (app *nativeApp) hasDiffFiles() bool {
	if app.repository == nil && app.ssh == nil || app.closed || app.busy || app.readTask != nil || app.dialog != nil {
		return false
	}
	working, files, _ := app.diffFileContext()
	if working {
		return len(app.fileSection(true).visible)+len(app.fileSection(false).visible) > 0
	}
	return len(files) > 0
}

func (app *nativeApp) orderedDiffFiles() []nativeDiffFile {
	working, files, _ := app.diffFileContext()
	order, descending := app.prefs.FileSort, app.prefs.FileDescending
	var entries []nativeDiffFile
	appendTree := func(tree *nativeFileTree, staged bool) {
		tree.visit(func(*nativeFileNode) bool { return false }, func(node *nativeFileNode) bool {
			if node.file != nil {
				entries = append(entries, nativeDiffFile{node.path, staged})
			}
			return true
		})
	}
	if working {
		for _, staged := range []bool{true, false} {
			sorted := app.fileSection(staged).sortedFiles(order, descending)
			if app.prefs.FileTree {
				appendTree(sorted.fileTree(staged), staged)
			} else {
				for _, file := range sorted.files {
					entries = append(entries, nativeDiffFile{file.Path, staged})
				}
			}
		}
	} else if app.prefs.FileTree {
		cache := nativeRevisionFiles{order: order, descending: descending}
		appendTree(cache.fileTree(files), false)
	} else {
		for _, file := range files {
			entries = append(entries, nativeDiffFile{path: file.Path})
		}
	}
	return entries
}

func (app *nativeApp) openDiffFile(file nativeDiffFile, direction int) {
	if !app.hasDiffFiles() {
		return
	}
	working, _, current := app.diffFileContext()
	if file == current && app.problem == "" {
		app.diffReveal = file
		return
	}
	if working && app.conflictDirty() {
		app.leaveConflict(func() { app.openDiffFile(file, direction) })
		return
	}
	switch {
	case working && app.ssh != nil:
		for _, entry := range app.ssh.snapshot.Files {
			if entry.Path == file.path {
				app.selectSSHFile(entry, file.staged, "")
				break
			}
		}
	case working && app.historyWorking && app.screen == "history":
		app.selectHistoryWorkingFile(file.path, file.staged)
	case working:
		app.selectFile(file.path, file.staged)
	case app.screen == "compare":
		app.selectComparisonFile(file.path)
	default:
		app.selectCommitFile(file.path)
	}
	if request := app.readTask; request != nil {
		request.onSuccess = func() {
			app.diffReveal = file
			count := app.diffNavigationHunks()
			if direction != 0 && count > 0 {
				index := 0
				if direction < 0 {
					index = count - 1
				}
				app.navigateDiffHunk(index)
			}
		}
	}
}

func (app *nativeApp) diffNavigationHunks() int {
	if len(app.diffViews) != 1 || hasImageDiff(app.diff) || app.diff.Generated && !app.fileViewer.generatedExpanded {
		return 0
	}
	return len(app.hunkOffsets)
}

func (app *nativeApp) navigateDiffHunk(index int) {
	app.hunk = index
	app.configureDiff(app.diffViews[0])
	app.goToPatchHunk()
}

func (app *nativeApp) diffHunkPosition() (int, bool, nativeDiffBoundary) {
	state, unified := app.diffViews[0], &app.unifiedDiffs[0]
	if state.Layout == editor.DiffUnified && unified.state.Buffer != nil {
		line := unified.state.CursorPos().Line
		index := sort.SearchInts(unified.hunkRows, line+1) - 1
		inside := index >= 0 && line < len(unified.rows) && unified.rows[line].gap == 0 && unified.rows[line].note == ""
		return index, inside, nativeDiffBoundary{cursor: unified.state.CursorPos(), layout: state.Layout}
	}
	run, inside := state.CurrentHunk()
	index := sort.SearchInts(app.hunkOffsets, run+1) - 1
	return index, inside, nativeDiffBoundary{cursor: state.CursorPos(), side: state.ActiveSide(), layout: state.Layout}
}

func (app *nativeApp) stepDiff(direction int) {
	if !app.hasDiffFiles() || app.conflict != nil || app.fileViewer.full && app.diff.New != nil {
		return
	}
	count := app.diffNavigationHunks()
	var boundary nativeDiffBoundary
	if count > 0 {
		index, inside, position := app.diffHunkPosition()
		boundary = position
		if direction > 0 || inside {
			index += direction
		}
		if index >= 0 && index < count {
			app.diffBoundary = nativeDiffBoundary{}
			app.navigateDiffHunk(index)
			return
		}
	}
	working, _, current := app.diffFileContext()
	entries := app.orderedDiffFiles()
	index := slices.Index(entries, current)
	if index < 0 && direction < 0 {
		index = len(entries)
	}
	var reviewed map[string]bool
	if !working {
		reviewed = app.reviewedFiles[app.revisionReviewScope()]
	}
	for index += direction; index >= 0 && index < len(entries); index += direction {
		file := entries[index]
		if reviewed[file.path] {
			continue
		}
		boundary.direction = direction
		if count > 0 && app.diffBoundary != boundary {
			app.diffBoundary = boundary
			return
		}
		app.diffBoundary = nativeDiffBoundary{}
		app.openDiffFile(file, direction)
		return
	}
	app.diffBoundary = nativeDiffBoundary{}
}

func (app *nativeApp) diffBoundaryNotice(c *ui.Context) {
	if app.diffBoundary.direction == 0 || app.diffNavigationHunks() == 0 {
		return
	}
	_, _, position := app.diffHunkPosition()
	position.direction = app.diffBoundary.direction
	if position != app.diffBoundary {
		return
	}
	label := locales.NextDiffFileHint()
	if position.direction < 0 {
		label = locales.PreviousDiffFileHint()
	}
	ui.Text(c, label).FontSize(11).TextColor(c.Theme().TextMuted).FillWidth().Padding(4, 12)
}

func (app *nativeApp) diffShortcuts(c *ui.Context) {
	if !app.hasDiffFiles() {
		return
	}
	c.OnWindowShortcut(0, ui.KeyF7, func() { app.stepDiff(1) })
	c.OnWindowShortcut(ui.Shift, ui.KeyF7, func() { app.stepDiff(-1) })
	bindings := app.prefs.Shortcuts
	for _, binding := range []string{bindings.Library, bindings.AddRepository, bindings.Scan, bindings.Refresh, bindings.Commit, bindings.QuickOpen} {
		if binding == "CmdOrCtrl+Shift+P" {
			return
		}
	}
	c.OnWindowShortcut(ui.Cmd|ui.Shift, ui.KeyP, app.openDiffFileFinder)
}
