// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

const (
	nativeSearchCommits = iota
	nativeSearchContent
	nativeSearchPaths
)

type nativeWorktreeSearch struct {
	query        string
	results      []gitrepo.WorktreeSearchMatch
	generation   uint64
	submitted    bool
	searched     bool
	openOnResult bool
	limited      bool
	selected     int
	list         ui.ListState
	input        ui.Handle
	focus        bool
}

func (app *nativeApp) searchWorkspace(c *ui.Context) {
	if app.repository == nil {
		return
	}
	ui.Column(c).Fill().MinHeight(0).Children(func() {
		ui.Row(c).FillWidth().Padding(8, 10).Children(func() {
			mode := app.searchMode
			ui.Segmented(c, &mode, locales.CommitHistory(), locales.WorktreeSearch(), locales.QuickOpen()).
				Label(locales.Search()).OnChange(func() { app.setSearchMode(mode) })
		})
		ui.Divider(c)
		switch app.searchMode {
		case nativeSearchContent:
			c.Key("worktree-search")
			app.worktreeSearchPanel(c, &app.contentSearch, false)
		case nativeSearchPaths:
			c.Key("quick-open")
			app.worktreeSearchPanel(c, &app.pathSearch, true)
		default:
			c.Key("commit-search")
			app.historyWorkspace(c)
		}
	})
}

func (app *nativeApp) setSearchMode(mode int) {
	if app.searchMode == mode {
		return
	}
	app.cancelRead()
	app.searchMode = mode
	if mode == nativeSearchPaths && !app.pathSearch.searched {
		app.startWorktreeSearch(&app.pathSearch, true, false)
	}
}

func (app *nativeApp) openQuickSearch() {
	if app.repository == nil || app.busy || app.closed || app.screen == "library" {
		return
	}
	app.leaveConflict(func() {
		app.switchWorkspace("search")
		app.setSearchMode(nativeSearchPaths)
		app.pathSearch.focus = true
		if app.readTask == nil {
			app.startWorktreeSearch(&app.pathSearch, true, false)
		}
	})
}

func (app *nativeApp) worktreeSearchPanel(c *ui.Context, state *nativeWorktreeSearch, paths bool) {
	reading := app.readTask != nil && app.readTask.kind == nativeReadWorktreeSearch
	label := locales.SearchWorktreeText()
	if paths {
		label = locales.FilterRepositoryFiles()
	}
	openSelected := func() {
		if state.selected >= 0 && state.selected < len(state.results) {
			app.openWorktreeSearchMatch(state.results[state.selected])
		}
	}
	submit := func() {
		if paths && state.searched && len(state.results) > 0 {
			openSelected()
			return
		}
		if !paths || !reading {
			app.startWorktreeSearch(state, paths, false)
		}
		state.openOnResult = paths
	}
	ui.Row(c).FillWidth().Padding(8, 10).Gap(8).AlignItems(ui.Center).Children(func() {
		if state.focus {
			state.input.Focus(c)
			state.focus = false
		}
		input := ui.TextInput(c, &state.query).Search().Label(label).Placeholder(label).Bind(&state.input).
			Grow(1).MinWidth(0).AutoFocus().OnChange(func() {
			state.resetResults()
			if app.readTask != nil && app.readTask.kind == nativeReadWorktreeSearch {
				app.cancelRead()
			}
			if paths {
				app.startWorktreeSearch(state, true, true)
			}
		}).OnSubmit(submit)
		if paths && len(state.results) > 0 {
			move := func(delta int) {
				row := min(len(state.results)-1, max(0, state.selected+delta))
				state.list.Choose(row)
				state.list.ScrollIntoView(row)
			}
			input.OnShortcut(0, ui.KeyDown, func() { move(1) }).
				OnShortcut(0, ui.KeyUp, func() { move(-1) })
		}
		if reading {
			ui.Spinner(c).Size(14, 14)
			ui.Button(c, locales.Cancel()).OnClick(app.cancelRead)
		} else if !paths {
			ui.PrimaryButton(c, locales.Search()).Disabled(state.query == "").OnClick(submit)
		}
	})
	ui.Divider(c)
	if len(state.results) == 0 {
		message := ""
		if !reading && state.searched {
			message = locales.NoMatchingFiles()
			if !paths {
				message = locales.NoWorktreeSearchMatches()
			}
		}
		ui.Column(c).FillWidth().Grow(1).MinHeight(0).Center().Children(func() {
			ui.Text(c, message).TextColor(c.Theme().TextMuted)
		})
		return
	}
	results := state.results
	state.list.Selected = &state.selected
	state.list.KeepOffset = true
	state.list.Key = func(index int) any {
		match := results[index]
		return struct {
			path string
			line int
		}{match.Path, match.Line}
	}
	state.list.Label = func(index int) string { return results[index].Path }
	list := ui.List(c, &state.list, len(results), func(index int) {
		match := results[index]
		ui.Column(c).FillWidth().Children(func() {
			if paths || index == 0 || results[index-1].Path != match.Path {
				ui.Row(c).FillWidth().Height(29).Padding(0, 12).Gap(8).AlignItems(ui.Center).Children(func() {
					nativeFileIcon(c, match.Path)
					path := ui.Text(c, match.Path).Grow(1).MinWidth(0).SingleLine().Tooltip(match.Path)
					if !paths {
						path.Bold().FontSize(12)
					}
				})
			}
			if !paths {
				ui.Row(c).FillWidth().Height(25).Padding(0, 12).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, fmt.Sprint(match.Line)).Width(32).FontSize(11).TextColor(c.Theme().TextMuted)
					ui.Text(c, strings.TrimSpace(match.Text)).Grow(1).MinWidth(0).FontSize(12).
						SingleLine().Tooltip(match.Text)
				})
			}
		})
	}).FillWidth().Grow(1).MinHeight(0).Label(label).OnSubmit(openSelected)
	if !paths {
		list.OnChange(openSelected)
	}
	if state.limited {
		ui.Text(c, locales.WorktreeSearchLimited(gitrepo.WorktreeSearchLimit)).
			FontSize(11).TextColor(c.Theme().TextMuted).Padding(6, 10)
	}
}

func (state *nativeWorktreeSearch) resetResults() {
	state.generation++
	state.results, state.searched, state.limited = nil, false, false
	state.submitted = false
	state.openOnResult = false
	state.selected, state.list = -1, ui.ListState{}
}

func (app *nativeApp) startWorktreeSearch(state *nativeWorktreeSearch, paths, debounce bool) {
	if app.repository == nil || app.busy || app.closed || app.screen != "search" {
		return
	}
	query := state.query
	if query == "" || paths && strings.TrimSpace(query) == "" {
		return
	}
	state.resetResults()
	state.submitted = true
	generation := state.generation
	repository, post := app.repository, app.post
	path := repository.snapshot.Path
	app.load(locales.WorktreeSearch(), nativeReadWorktreeSearch, func(ctx context.Context) (func(), error) {
		if debounce {
			timer := time.NewTimer(150 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		repo, err := app.service.manager.Get(path)
		if err != nil {
			return nil, err
		}
		if paths {
			results, err := quickWorktreeFiles(ctx, repo, query)
			return func() {
				state.results, state.searched = results, err == nil
				if len(results) > 0 {
					state.selected = 0
					if state.openOnResult && err == nil {
						app.openWorktreeSearchMatch(results[0])
					}
				}
				state.openOnResult = false
			}, err
		}
		var matches []gitrepo.WorktreeSearchMatch
		var lastUpdate time.Time
		limited, err := repo.SearchWorktree(ctx, query, func(batch []gitrepo.WorktreeSearchMatch) {
			matches = append(matches, batch...)
			if time.Since(lastUpdate) < 150*time.Millisecond {
				return
			}
			lastUpdate = time.Now()
			results := rankWorktreeMatches(matches, query)
			post(func() {
				if !app.closed && app.repository == repository && app.screen == "search" && app.searchMode == nativeSearchContent &&
					state.generation == generation && app.readTask != nil && app.readTask.kind == nativeReadWorktreeSearch {
					state.results = results
				}
			})
		})
		results := rankWorktreeMatches(matches, query)
		return func() { state.results, state.searched, state.limited = results, err == nil, limited }, err
	})
}

func quickWorktreeFiles(ctx context.Context, repo *gitrepo.Repository, query string) ([]gitrepo.WorktreeSearchMatch, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	paths, err := repo.WorktreeSearchPaths(ctx)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(repo.Path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var results []gitrepo.WorktreeSearchMatch
	for _, ranked := range rankQuickPaths(ctx, paths, query) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := root.Lstat(ranked.path)
		if err == nil && !info.IsDir() {
			results = append(results, gitrepo.WorktreeSearchMatch{Path: ranked.path})
			if len(results) == 200 {
				break
			}
		}
	}
	return results, ctx.Err()
}

func (app *nativeApp) openWorktreeSearchMatch(match gitrepo.WorktreeSearchMatch) {
	if app.repository == nil || app.busy || app.screen != "search" {
		return
	}
	if app.browser == nil {
		app.browser = newNativeBrowser()
	}
	app.browser.jump = &nativeBrowserJump{
		revision: "WORKTREE", file: match.Path,
		position: editor.Pos{Line: max(0, match.Line-1), Col: match.Column},
	}
	app.switchWorkspace("files")
}
