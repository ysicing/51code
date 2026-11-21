// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func (app *nativeApp) historyWorkingOffset() int {
	if app.screen == "history" && app.repository != nil && len(app.repository.snapshot.Files) > 0 && app.historyOffset == 0 &&
		app.historyResult.query == "" && app.historyResult.file == "" {
		return 1
	}
	return 0
}

func historyGraphWithWorking(commits []gitrepo.Commit, head string, working bool) []historyGraphRow {
	if !working {
		return commitGraph(commits)
	}
	rows := make([]gitrepo.Commit, 1, len(commits)+1)
	rows[0] = historyWorkingCommit(head)
	rows = append(rows, commits...)
	graph := commitGraph(rows)
	graph[0].Working = true
	return graph
}

func historyWorkingFile(files []gitrepo.File, selected string, staged bool) (string, bool) {
	if selected != "" && fileExists(files, selected, staged) {
		return selected, staged
	}
	if selected != "" && fileExists(files, selected, !staged) {
		return selected, !staged
	}
	for _, file := range files {
		if isWorking(file) {
			return file.Path, false
		}
	}
	for _, file := range files {
		if isStaged(file) {
			return file.Path, true
		}
	}
	return "", false
}

func (app *nativeApp) selectHistoryWorking() {
	if app.historyWorkingOffset() == 0 || app.busy {
		return
	}
	file, staged := historyWorkingFile(app.repository.snapshot.Files, app.selectedFile, app.staged)
	app.selectHistoryWorkingFile(file, staged)
}

func (app *nativeApp) selectHistoryWorkingFile(file string, staged bool) {
	if app.repository == nil || app.screen != "history" || app.historyWorkingOffset() == 0 {
		return
	}
	path := app.repository.snapshot.Path
	files := app.repository.snapshot.Files
	app.load(locales.ReadingDiff(), nativeReadDiff, func(ctx context.Context) (func(), error) {
		selection, err := app.readHistoryWorkingSelection(ctx, path, file, staged, files)
		return func() {
			app.applyHistoryWorkingSelection(selection)
		}, err
	})
}

func (app *nativeApp) readHistoryWorkingSelection(ctx context.Context, path, file string, staged bool, files []gitrepo.File) (*nativeSelection, error) {
	selection := &nativeSelection{file: file, staged: staged}
	for _, entry := range files {
		if entry.Path == file && entry.Conflict {
			diff, err := app.service.Diff(ctx, path, file, staged)
			selection.diff = diff
			return selection, err
		}
	}
	result, err := app.readSelection(ctx, path, file, staged, files)
	if err != nil {
		return selection, err
	}
	return result, nil
}

func (app *nativeApp) applyHistoryWorkingSelection(selection *nativeSelection) {
	app.setSelection(selection)
	app.historyWorking = true
	app.historySelected, app.commitSelected = -1, -1
	app.commitHash, app.commitFile, app.commitFiles = "", "", nil
	app.viewedRef, app.viewedCommit = nativeRefKey{}, gitrepo.Commit{}
}

func (app *nativeApp) openHistoryWorkingChanges() {
	if app.repository == nil || app.busy || app.readTask != nil {
		return
	}
	file, staged := app.selectedFile, app.staged
	app.switchWorkspace("changes")
	app.refSelected = nativeRefKey{}
	app.fileQuery, app.fileFilter = "", ""
	clear(app.fileCollapsed)
	app.clearDiff()
	if file != "" {
		app.selectFile(file, staged)
	}
}

func (app *nativeApp) historyWorkingFiles(c *ui.Context) {
	app.changesFileSections(c)
}
