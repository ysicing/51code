// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"path"
	"slices"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/internal/workspace"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

type nativeSSHWorkspace struct {
	repo           gitrepo.SSHWorkspace
	snapshot       gitrepo.Snapshot
	refs           []string
	comparisonRefs []string
	ref            string
	historyFile    string
	stashes        []gitrepo.Stash
}

func (state *nativeSSHWorkspace) address() string { return state.repo.Host + ":" + state.repo.Path }

func (state *nativeSSHWorkspace) title() string {
	return path.Base(state.repo.Path) + " · " + state.repo.Host
}

func (state *nativeSSHWorkspace) update(snapshot gitrepo.Snapshot, refs []string) {
	state.snapshot, state.refs = snapshot, refs
	state.comparisonRefs = append([]string{"HEAD"}, refs...)
}

func (app *nativeApp) connectSSHRepository(saved gitrepo.SSHWorkspace) {
	if app.screen != "library" || app.busy || app.closed || app.dialog != nil {
		return
	}
	app.sshRepositoryPicker(saved)
}

func (app *nativeApp) openSSHRepository(form *nativeDialog, repo gitrepo.SSHWorkspace) {
	state := &nativeSSHWorkspace{repo: repo}
	key := "ssh:" + state.address()
	if form.cancel != nil {
		form.cancel()
		form.cancel = nil
	}
	if app.desktop != nil {
		if existing := app.desktop.repositories[key]; existing != nil && !existing.closed && slices.Contains(app.library.SSHRepositories, state.repo) {
			app.closeDialog(form)
			app.desktop.present(existing)
			return
		}
	}
	ctx, cancel := context.WithCancel(app.ctx)
	form.cancel, form.blocked, form.problem = cancel, true, ""
	go func() {
		snapshot, refs, err := state.repo.Snapshot(ctx)
		var library workspace.Library
		if err == nil {
			library, err = app.service.manager.RememberSSH(ctx, state.repo)
		}
		app.post(func() {
			defer cancel()
			if app.closed || app.dialog != form || ctx.Err() != nil {
				return
			}
			form.cancel, form.blocked = nil, false
			if err != nil {
				form.problem = err.Error()
				return
			}
			app.closeDialog(form)
			app.library = library
			window := app
			if app.desktop != nil {
				if existing := app.desktop.repositories[key]; existing != nil && !existing.closed {
					app.desktop.present(existing)
					return
				}
				window = app.desktop.newApp()
				app.desktop.repositories[key] = window
			}
			state.update(snapshot, refs)
			window.repository, window.writable = nil, false
			window.ssh, window.screen = state, "sshChanges"
			if app.desktop != nil {
				app.desktop.present(window)
			}
			window.selectSSHWorkingFile()
		})
	}()
}

func (app *nativeApp) sshLibraryView(c *ui.Context) {
	ui.Column(c).FillWidth().Grow(1).MinHeight(0).Padding(22).Gap(16).Children(func() {
		ui.TextInput(c, &app.remoteQuery).Search().Placeholder(locales.SearchSshRepositories()).Label(locales.SearchSshRepositories()).FillWidth()
		query := strings.ToLower(strings.TrimSpace(app.remoteQuery))
		if len(app.library.SSHRepositories) == 0 {
			ui.Column(c).Fill().Center().Children(func() { ui.Text(c, locales.NoSshRepositories()).TextColor(c.Theme().TextMuted) })
			return
		}
		matched := false
		ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
			for _, repo := range app.library.SSHRepositories {
				if !strings.Contains(strings.ToLower(repo.Host+":"+repo.Path), query) {
					continue
				}
				matched = true
				name, address := path.Base(repo.Path), repo.Host+" · "+repo.Path
				ui.Row(c).Key(repo).FillWidth().Padding(4, 0).Gap(8).AlignItems(ui.Center).Children(func() {
					listRow(c, name, false).Description(address).Grow(1).Padding(14, 12).Children(func() {
						ui.Icon(c, nativeIcons["cloud"]).FontSize(24).TextColor(c.Theme().TextMuted)
						ui.Column(c).Grow(1).MinWidth(0).Gap(5).Children(func() {
							ui.Text(c, name).FontSize(14).Bold().SingleLine()
							ui.Text(c, address).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine().Tooltip(address)
						})
					}).OnClick(func() { app.connectSSHRepository(repo) })
					iconButton(c, locales.Manage()+" "+address, nativeIcons["more"]).Menu(func(menu *ui.Menu) {
						if menu.Item(locales.RemoveFromListMenu()).Chosen() {
							app.confirm(locales.RemoveFromListMenu(), address, locales.Remove(), func() {
								app.libraryChange(locales.RemovingRecord(), func(context.Context) (workspace.Library, error) {
									return app.service.manager.ForgetSSH(repo)
								})
							})
						}
					})
				})
				ui.Divider(c)
			}
			if !matched {
				ui.Column(c).FillWidth().Center().Gap(8).Padding(22).Children(func() {
					ui.Text(c, locales.NoMatchingRepositories()).TextColor(c.Theme().TextMuted)
					ui.Button(c, locales.ClearSearch()).OnClick(func() { app.remoteQuery = "" })
				})
			}
		})
	})
}

func (app *nativeApp) refreshSSH() {
	state := app.ssh
	app.load(locales.OpeningRepository(), nativeReadDiff, func(ctx context.Context) (func(), error) {
		snapshot, refs, err := state.repo.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		return func() {
			state.update(snapshot, refs)
			app.fileMetrics = nativeFileMetrics{}
			switch app.screen {
			case "worktrees":
				app.loadWorktrees()
			case "submodules":
				app.loadSubmodules()
			case "stash":
				app.loadSSHStashes()
			case "sshChanges":
				app.selectSSHWorkingFile()
			case "files":
				app.loadBrowser(app.browser.revision, app.browser.file, nil)
			case "insights":
				app.refreshInsights(true)
			case "sshHistory":
				if strings.HasPrefix(state.ref, "refs/") && !slices.Contains(refs, state.ref) {
					state.ref = ""
				}
				app.loadSSHHistory()
			case "compare":
				app.startComparison()
			case "search":
				app.pathSearch.invalidateQuickPaths()
				switch app.searchMode {
				case nativeSearchCommits:
					app.searchHistory(false)
				case nativeSearchPaths:
					app.startWorktreeSearch(&app.pathSearch, true, false)
				case nativeSearchContent:
					app.startWorktreeSearch(&app.contentSearch, false, false)
				}
			}
		}, nil
	})
}

func (app *nativeApp) switchSSH(selected string) {
	switch selected {
	case "screen:worktrees":
		app.switchWorkspace("worktrees")
		app.loadWorktrees()
		return
	case "screen:submodules":
		app.switchWorkspace("submodules")
		app.loadSubmodules()
		return
	case "screen:stash":
		app.switchWorkspace("stash")
		app.loadSSHStashes()
		return
	case "screen:compare", "screen:search":
		app.switchWorkspace(strings.TrimPrefix(selected, "screen:"))
		return
	case "screen:files":
		app.switchWorkspace("files")
		return
	case "screen:insights":
		app.switchWorkspace("insights")
		return
	case "screen:sshChanges":
		app.switchWorkspace("sshChanges")
		app.selectSSHWorkingFile()
		return
	}
	ref := ""
	if strings.HasPrefix(selected, "refs/") {
		ref = selected
	}
	if app.ssh.ref == ref && app.ssh.historyFile == "" {
		app.switchWorkspace("sshHistory")
		if !app.historyLoaded {
			app.loadSSHHistory()
		}
		return
	}
	app.startSSHHistory(ref, "")
}

func (app *nativeApp) startSSHHistory(ref, file string) {
	app.switchWorkspace("sshHistory")
	app.cancelRead()
	app.clearDiff()
	app.selectedFile = ""
	app.ssh.ref, app.ssh.historyFile = ref, file
	app.nativeHistoryState = newNativeHistoryState()
	app.loadSSHHistory()
}

func (app *nativeApp) loadSSHHistory() {
	app.readSSHHistoryPage(false)
}

func (app *nativeApp) readSSHHistoryPage(more bool) {
	var request nativeHistoryRequest
	ref := ""
	if app.screen == "sshHistory" {
		ref, request.file = app.ssh.ref, app.ssh.historyFile
		if request.file != "" && (ref == "WORKTREE" || ref == "INDEX" || ref == "HEAD") {
			ref = ""
		}
	} else if more {
		request = app.historyResult
	} else {
		request = app.historyRequest()
	}
	freshSearch := app.screen == "search" && !more && (!app.historyLoaded || request != app.historyResult)
	repo := app.ssh.repo
	kind, skip, limit, label := nativeReadHistory, 0, gitrepo.DefaultHistoryPageSize, locales.SearchingHistory()
	if more {
		kind, skip, limit, label = nativeReadHistoryPage, len(app.history), gitrepo.MaxHistoryPageSize, locales.LoadingHistory()
	}
	app.load(label, kind, func(ctx context.Context) (func(), error) {
		history, err := repo.HistoryPage(ctx, ref, request.query, request.file, request.filter, skip, limit)
		if err != nil {
			return func() { app.historyPagePaused = more }, err
		}
		return func() {
			app.historyMore, app.historyLoaded, app.historyPagePaused = len(history) == limit, true, false
			app.historyResult = request
			if more {
				app.historyList.HoldPlace()
				app.history = append(app.history, history...)
				return
			}
			app.history = history
			index := slices.IndexFunc(history, func(commit gitrepo.Commit) bool { return commit.Hash == app.commitHash })
			if freshSearch {
				app.historyList, app.commitList = &ui.ListState{}, &ui.ListState{}
			}
			if len(history) == 0 || app.screen == "search" && (freshSearch || index < 0) {
				app.commitHash, app.commitParent, app.commitFiles, app.selectedFile, app.commitFile, app.historySelected = "", "", nil, "", "", -1
				app.commitSelected = -1
				app.clearDiff()
				return
			}
			app.selectSSHCommit(history[max(0, index)].Hash)
		}, nil
	})
}

func (app *nativeApp) selectSSHCommit(hash string) {
	parent := ""
	if hash == app.commitHash {
		parent = app.commitParent
	}
	app.loadSSHCommit(hash, parent)
}

func (app *nativeApp) loadSSHCommit(hash, parent string) {
	repo := app.ssh.repo
	previous := app.ssh.historyFile
	if hash == app.commitHash {
		previous = app.selectedFile
	}
	app.historySelected = slices.IndexFunc(app.history, func(commit gitrepo.Commit) bool { return commit.Hash == hash })
	app.load(locales.CommitFileList(), nativeReadDiff, func(ctx context.Context) (func(), error) {
		var files []gitrepo.CommitFile
		var err error
		if parent == "" {
			files, err = repo.CommitFiles(ctx, hash)
		} else {
			var result gitrepo.Comparison
			result, err = repo.Compare(ctx, parent, hash, false)
			files = result.Files
		}
		if err != nil {
			return nil, err
		}
		return func() {
			app.clearDiff()
			app.commitParent, app.commitHash, app.commitFiles, app.commitFile, app.selectedFile, app.commitSelected = parent, hash, files, "", "", -1
			if len(files) > 0 {
				index := slices.IndexFunc(files, func(file gitrepo.CommitFile) bool { return file.Path == previous })
				app.selectSSHCommitFile(files[max(0, index)])
			}
		}, nil
	})
}

func (app *nativeApp) selectSSHWorkingFile() {
	name, staged := historyWorkingFile(app.ssh.snapshot.Files, app.selectedFile, app.staged)
	for _, file := range app.ssh.snapshot.Files {
		if file.Path == name {
			app.selectSSHFile(file, staged, "")
			return
		}
	}
	app.selectedFile = ""
	app.clearDiff()
}

func (app *nativeApp) selectSSHCommitFile(file gitrepo.CommitFile) {
	app.commitFile = file.Path
	app.commitSelected = slices.IndexFunc(app.commitFiles, func(entry gitrepo.CommitFile) bool { return entry.Path == file.Path })
	parent := app.commitParent
	if parent == "" {
		if commit := app.selectedHistoryCommit(); commit != nil && len(commit.Parents) > 0 {
			parent = commit.Parents[0]
		}
	}
	if parent != "" {
		repo, hash := app.ssh.repo, app.commitHash
		app.clearDiff()
		app.selectedFile = file.Path
		app.load(locales.ReadingFileDiff(), nativeReadDiff, func(ctx context.Context) (func(), error) {
			diff, err := repo.ComparisonDiff(ctx, parent, hash, file)
			if err != nil {
				return nil, err
			}
			return func() { app.setDiff(diff) }, nil
		})
		return
	}
	app.selectSSHFile(gitrepo.File{Path: file.Path, OriginalPath: file.OriginalPath}, false, app.commitHash)
}

func (app *nativeApp) selectSSHFile(file gitrepo.File, staged bool, hash string) {
	app.clearDiff()
	app.selectedFile, app.staged = file.Path, staged
	repo := app.ssh.repo
	app.load(locales.ReadingDiff(), nativeReadDiff, func(ctx context.Context) (func(), error) {
		diff, err := repo.FileDiff(ctx, file, staged, hash)
		if err != nil {
			return nil, err
		}
		return func() { app.setDiff(diff) }, nil
	})
}

func (app *nativeApp) sshSidebar(c *ui.Context) {
	ui.SidebarSection(c, locales.Workspace(), nil, func() {
		ui.SidebarItem(c, "screen:sshChanges", nativeIcons["file"], locales.FileChanges())
		ui.SidebarItem(c, "screen:sshHistory", nativeIcons["history"], locales.CommitHistory())
		ui.SidebarItem(c, "screen:files", nativeIcons["folder"], locales.RepositoryFiles())
		if app.prefs.SidebarItems.Insights {
			ui.SidebarItem(c, "screen:insights", nativeIcons["insights"], locales.Insights())
		}
		if app.prefs.SidebarItems.Search {
			ui.SidebarItem(c, "screen:search", nativeIcons["search"], locales.Search())
		}
		if app.prefs.SidebarItems.Compare {
			ui.SidebarItem(c, "screen:compare", nativeIcons["branch"], locales.CompareVersions())
		}
		if app.prefs.SidebarItems.Stash {
			ui.SidebarItem(c, "screen:stash", nativeIcons["stash"], locales.Stash())
		}
		if app.prefs.SidebarItems.Worktrees {
			ui.SidebarItem(c, "screen:worktrees", nativeIcons["folder"], locales.Worktrees())
		}
		if app.prefs.SidebarItems.Submodules {
			ui.SidebarItem(c, "screen:submodules", nativeIcons["folder"], locales.Submodules())
		}
	})
	for _, group := range []struct{ prefix, label, icon string }{{"refs/heads/", locales.LocalBranches(), "branch"}, {"refs/tags/", locales.Tags(), "tag"}, {"refs/remotes/", locales.Remotes(), "branch"}} {
		ui.SidebarSection(c, group.label, nil, func() {
			for _, ref := range app.ssh.refs {
				if name, ok := strings.CutPrefix(ref, group.prefix); ok {
					ui.SidebarItem(c, ref, nativeIcons[group.icon], name).Tooltip(name)
				}
			}
		})
	}
}

func (app *nativeApp) sshPanel(c *ui.Context) {
	if app.screen == "sshChanges" {
		ui.Split(c, &app.prefs.Files, func() {
			app.fileControls(c)
			app.changesFileSections(c)
		}, func() { app.fileDiffPanel(c) }).Fill()
		return
	}
	app.sshHistoryPanel(c)
}

func (app *nativeApp) loadSSHStashes() {
	state := app.ssh
	app.load(locales.ReadingStash(), nativeReadCommit, func(ctx context.Context) (func(), error) {
		items, err := state.repo.Stashes(ctx)
		if err != nil {
			return nil, err
		}
		return func() {
			state.stashes = items
			if slices.ContainsFunc(items, func(item gitrepo.Stash) bool { return item.Hash == app.stashHash }) {
				app.selectStash(app.stashHash)
			} else {
				app.stashHash = ""
				app.clearDiff()
			}
		}, nil
	})
}

func (app *nativeApp) openRelatedWorktree(directory string) {
	if app.ssh == nil {
		app.openRepository(directory)
		return
	}
	if app.desktop != nil {
		library := app.desktop.showLibrary()
		library.remoteLibrary, library.group = true, ""
		library.connectSSHRepository(gitrepo.SSHWorkspace{Host: app.ssh.repo.Host, Path: directory})
	}
}

func (app *nativeApp) sshHistoryPanel(c *ui.Context) {
	ui.SplitVerticalPercent(c, &app.prefs.History, func() {
		ui.Column(c).Fill().Children(func() {
			if app.screen == "search" {
				app.searchControls(c)
			} else if file := app.ssh.historyFile; file != "" {
				ui.Row(c).FillWidth().Gap(8).Padding(6, 12).AlignItems(ui.Center).Children(func() {
					ui.Text(c, locales.FileHistory()).Bold()
					ui.Text(c, file).SingleLine().MinWidth(0).Grow(1).Tooltip(file)
					ui.Button(c, locales.ClearSearch()).OnClick(func() { app.startSSHHistory("", "") })
					app.historyReadControls(c)
				})
			} else if app.readTask != nil && (app.readTask.kind == nativeReadHistory || app.readTask.kind == nativeReadHistoryPage) {
				ui.Row(c).Padding(6, 12).Gap(8).AlignItems(ui.Center).Children(func() { app.historyReadControls(c) })
			}
			if len(app.history) == 0 {
				if app.readTask == nil {
					message := locales.NoMatchingCommits()
					if app.screen == "search" && !app.historyLoaded {
						message = locales.SearchStartHint()
					}
					ui.Text(c, message).Padding(12)
				}
				return
			}
			app.historyRows(c, app.ssh.snapshot.Head, 0, 0, app.selectSSHCommit)
			if app.screen == "search" {
				ui.Text(c, app.searchSummary()).FontSize(11).TextColor(c.Theme().TextMuted).Padding(4, 10)
			}
			if app.historyPagePaused && app.historyMore {
				ui.Button(c, locales.Retry()).OnClick(func() { app.readSSHHistoryPage(true) })
			}
			app.loadHistoryNearEnd(c)
		})
	}, func() {
		ui.Column(c).Fill().Children(func() {
			app.historyDetails(c)
			ui.Divider(c)
			ui.Split(c, &app.prefs.HistoryFiles, func() { app.historyFiles(c) }, func() { app.fileDiffPanel(c) }).Fill()
		})
	}).Fill()
}
