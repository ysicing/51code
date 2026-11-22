// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"reflect"
	"slices"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

type nativeBrowser struct {
	files      []string
	revision   string
	file       string
	query      string
	viewer     nativeFileViewer
	collapsed  map[string]bool
	outline    *nativeFileOutline
	cache      nativeRevisionFiles
	filtered   []gitrepo.CommitFile
	filterKey  string
	jump       *nativeBrowserJump
	previous   string
	history    nativeHistoryState
	pending    bool
	blame      *nativeBrowserLineBlame
	shownBlame *nativeBrowserLineBlame
}

type nativeBrowserJump struct {
	revision, file string
	position       editor.Pos
}

type nativeBrowserResult struct {
	files    []string
	revision string
	file     string
	diff     gitrepo.Diff
}

func newNativeBrowser() *nativeBrowser {
	return &nativeBrowser{
		revision:  "WORKTREE",
		collapsed: map[string]bool{}, outline: newNativeFileOutline(),
	}
}

func (state *nativeBrowser) visibleFiles() []gitrepo.CommitFile {
	query := strings.ToLower(state.query)
	if state.filtered != nil && state.filterKey == query {
		return state.filtered
	}
	state.filtered, state.filterKey = make([]gitrepo.CommitFile, 0, len(state.files)), query
	for _, path := range state.files {
		if query == "" || strings.Contains(strings.ToLower(path), query) {
			state.filtered = append(state.filtered, gitrepo.CommitFile{Path: path})
		}
	}
	state.cache = nativeRevisionFiles{}
	return state.filtered
}

func (app *nativeApp) readBrowser(ctx context.Context, path, revision, file string) (*nativeBrowserResult, error) {
	files, err := app.service.BrowseFiles(ctx, path, revision)
	if err != nil {
		return nil, err
	}
	result := &nativeBrowserResult{files: files, revision: revision, file: file}
	return app.readBrowserContent(ctx, path, result)
}

func (app *nativeApp) readBrowserContent(ctx context.Context, path string, result *nativeBrowserResult) (*nativeBrowserResult, error) {
	file, revision := result.file, result.revision
	if file == "" {
		return result, nil
	}
	var err error
	result.diff.New, err = app.service.BrowseFileContent(ctx, path, file, revision)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (state *nativeBrowser) apply(result *nativeBrowserResult) bool {
	changed := !slices.Equal(state.files, result.files) || state.file != result.file ||
		state.revision != result.revision || !reflect.DeepEqual(state.viewer.sources,
		[2]*gitrepo.FileContent{result.diff.Old, result.diff.New})
	if !slices.Equal(state.files, result.files) {
		state.files = result.files
		state.filtered = nil
	}
	state.revision, state.file = result.revision, result.file
	state.pending, state.outline.pinned = false, false
	if result.file != "" {
		state.viewer.setSources(result.diff)
	} else {
		state.viewer = nativeFileViewer{}
	}
	return changed
}

func (app *nativeApp) loadBrowser(revision, file string, position *editor.Pos) {
	if app.repository == nil || app.browser == nil || app.screen != "files" {
		return
	}
	state, path := app.browser, app.repository.snapshot.Path
	state.pending = true
	files := state.files
	contentOnly := file != "" && file != state.file && revision == state.revision && files != nil && position == nil
	app.load(locales.RepositoryFiles(), nativeReadDiff, func(ctx context.Context) (func(), error) {
		var result *nativeBrowserResult
		var err error
		if contentOnly {
			result, err = app.readBrowserContent(ctx, path, &nativeBrowserResult{files: files, revision: revision, file: file})
		} else {
			result, err = app.readBrowser(ctx, path, revision, file)
		}
		if err != nil {
			return nil, err
		}
		return func() {
			if app.browser != state {
				return
			}
			state.apply(result)
			if position != nil {
				state.viewer.side = 1
				if viewer := state.viewer.state(1); viewer.Buffer != nil {
					viewer.GoTo(*position)
				}
			}
		}, nil
	})
}

func (app *nativeApp) selectBrowserFile(file string) {
	if app.browser == nil || file == app.browser.file && app.problem == "" && app.readTask == nil {
		return
	}
	app.loadBrowser(app.browser.revision, file, nil)
}

func (app *nativeApp) browserVersionMenu(menu *ui.Menu) {
	state := app.browser
	versions := []string{"WORKTREE", "HEAD", "INDEX"}
	if !slices.Contains(versions, state.revision) {
		versions = append(versions, state.revision)
	}
	for _, revision := range versions {
		label := fileVersionLabel(&gitrepo.FileContent{Revision: revision})
		if menu.Item(label).Radio(state.revision == revision).Chosen() {
			file := state.file
			// 更换版本时新文件可能不存在，先显示版本的目录，再由用户选文件。
			if state.revision != revision {
				file = ""
			}
			app.loadBrowser(revision, file, nil)
		}
	}
}

func (app *nativeApp) browserWorkspace(c *ui.Context) {
	state := app.browser
	if state == nil {
		return
	}
	if state.pending && app.readTask == nil {
		state.pending, state.outline.pinned = false, false
		state.outline.cursor = state.outline.row("f:" + state.file)
	}
	ui.Split(c, &app.prefs.Files, func() {
		ui.Column(c).Fill().Children(func() {
			ui.Column(c).FillWidth().Padding(10, 12).Gap(8).Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, locales.RepositoryFiles()).Bold().Grow(1)
					ui.MenuButton(c, fileVersionLabel(&gitrepo.FileContent{Revision: state.revision}), app.browserVersionMenu).
						Label(locales.RepositoryFiles()).Disabled(app.busy)
					if app.readTask != nil {
						ui.Spinner(c).Size(14, 14)
						ui.Button(c, locales.Cancel()).OnClick(app.cancelRead)
					}
				})
				ui.TextInput(c, &state.query).Search().Label(locales.FilterRepositoryFiles()).
					Placeholder(locales.FilterRepositoryFiles()).FillWidth()
			})
			ui.Divider(c)
			files := state.visibleFiles()
			if len(files) == 0 {
				message := locales.EmptyRepositoryFiles()
				if state.query != "" {
					message = locales.NoMatchingFiles()
				}
				ui.Column(c).Grow(1).FillWidth().Center().Padding(20).Children(func() {
					ui.Text(c, message).TextColor(c.Theme().TextMuted)
				})
				return
			}
			state.cache.sort(files, "path", false)
			selected := -1
			if index, ok := state.cache.indices[state.file]; ok {
				selected = index
			}
			app.revisionFileTree(c, state.cache.fileTree(files), state.outline, state.collapsed,
				state.cache.indices, &selected, state.file, locales.RepositoryFiles(), app.busy, app.selectBrowserFile)
		})
	}, func() {
		app.browserFilePanel(c)
	}).Grow(1).MinHeight(0).FillWidth()
}

func (app *nativeApp) browserFilePanel(c *ui.Context) {
	state := app.browser
	ui.Column(c).Fill().MinWidth(0).Children(func() {
		ui.Row(c).FillWidth().Padding(8, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, state.file).Bold().Grow(1).MinWidth(0).MaxLines(1).Tooltip(state.file)
			if state.file != "" {
				ui.Button(c, locales.FileHistory()).OnClick(func() {
					file := state.file
					app.switchWorkspace("search")
					app.historyFile, app.historyQuery, app.historyFilter = file, "", gitrepo.HistoryFilter{}
					app.searchDateEnabled = false
					app.executeSearch()
				})
			}
		})
		viewer := state.viewer.state(state.viewer.side)
		ui.Row(c).FillWidth().Padding(0, 12, 6).Children(func() {
			ui.Button(c, locales.FindInFile()).Disabled(viewer.Buffer == nil).
				OnClick(func() { viewer.Run(editor.Find) })
		})
		ui.Divider(c)
		if viewer.Buffer != nil && state.file != "" {
			viewer.SoftWrap = app.prefs.Wrap
			viewer.ContextMenu = func(menu *ui.Menu) { app.browserBlameMenu(state, viewer, menu) }
			editor.View(c, viewer).Grow(1).FillWidth().MinHeight(0)
			app.updateBrowserBlame(state, viewer)
			return
		}
		message := locales.SelectFileOrCommit()
		if source := state.viewer.sources[state.viewer.side]; state.file != "" && source != nil {
			message = locales.AbsentVersion()
			switch {
			case source.Unsupported:
				message = locales.FileContentUnsupported()
			case source.Truncated:
				message = locales.FileContentTooLarge()
			case source.Binary:
				message = locales.BinaryFile()
			}
		}
		ui.Column(c).Grow(1).FillWidth().Center().Padding(20).Children(func() {
			ui.Text(c, message).TextColor(c.Theme().TextMuted)
		})
	})
}
