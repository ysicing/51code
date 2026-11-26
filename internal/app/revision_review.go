// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

type revisionReviewScope struct {
	repository, base, target string
}

func (app *nativeApp) revisionReviewScope() revisionReviewScope {
	if app.repository != nil {
		path := app.repository.snapshot.Path
		switch app.screen {
		case "history", "search":
			if !app.historyWorking && app.commitHash != "" && app.diff.Old != nil &&
				app.diff.New != nil && app.diff.New.Revision == app.commitHash {
				return revisionReviewScope{path, app.diff.Old.Revision, app.commitHash}
			}
		case "compare":
			if state := app.comparison; state != nil && state.result != nil {
				return revisionReviewScope{path, state.result.From, state.result.Target}
			}
		}
	}
	return revisionReviewScope{}
}

func (app *nativeApp) toggleRevisionReviewed(scope revisionReviewScope, files []gitrepo.CommitFile, index int) int {
	if scope.target == "" || index < 0 || index >= len(files) {
		return -1
	}
	if app.reviewedFiles == nil {
		app.reviewedFiles = make(map[revisionReviewScope]map[string]bool)
	}
	marked := app.reviewedFiles[scope]
	if marked == nil {
		marked = make(map[string]bool)
		app.reviewedFiles[scope] = marked
	}
	path := files[index].Path
	if marked[path] {
		delete(marked, path)
		return -1
	}
	marked[path] = true
	for step := 1; step < len(files); step++ {
		next := (index + step) % len(files)
		if !marked[files[next].Path] {
			return next
		}
	}
	return -1
}

func (app *nativeApp) reviewedFileBadge(c *ui.Context, path string) {
	if app.reviewedFiles[app.revisionReviewScope()][path] {
		ui.Text(c, "✓").Label(locales.FileReviewed()).Tooltip(locales.FileReviewed()).TextColor(c.Theme().Accent)
	}
}

func (outline *nativeFileOutline) revealFile(path string, collapsed map[string]bool) {
	if outline.tree == nil {
		return
	}
	for _, dir := range outline.tree.dirs {
		if strings.HasPrefix(path, dir.path+"/") && !outline.state.Open.Has(dir.key) {
			outline.toggle(dir, collapsed)
		}
	}
	if row := outline.row("f:" + path); row >= 0 {
		outline.state.List.ScrollIntoView(row)
	}
}
