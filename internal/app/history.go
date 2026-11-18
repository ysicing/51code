// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

type historyLane struct {
	Hash  string
	Color int
}

type historyEdge struct {
	From, To int
	Color    int
}

type historyGraphRow struct {
	Working                bool
	Column, Color, Columns int
	Incoming               []int
	Edges                  []historyEdge
}

// commitGraph 一次性计算提交图，算法见 historyGraphBuilder。
func commitGraph(commits []gitrepo.Commit) []historyGraphRow {
	rows, _ := newHistoryGraphBuilder().append(commits)
	return rows
}

func historyColor(theme *ui.Theme, index int) ui.Color {
	colors := []ui.Color{
		{R: 0, G: 113, B: 245, A: 255},
		{R: 226, G: 120, B: 22, A: 255},
		{R: 51, G: 153, B: 72, A: 255},
		{R: 160, G: 87, B: 210, A: 255},
		{R: 0, G: 151, B: 167, A: 255},
	}
	color := colors[index%len(colors)]
	if theme.Dark {
		color = color.Mix(ui.Color{R: 255, G: 255, B: 255, A: 255}, 0.22)
	}
	return color
}

func drawHistoryGraph(p *ui.Painter, r ui.Rect, row historyGraphRow, theme *ui.Theme) {
	x := func(column int) float32 { return r.X + 8 + float32(column)*16 }
	middle := r.Y + r.H/2
	for column, color := range row.Incoming {
		p.Line(x(column), r.Y, x(column), middle, 1.6, historyColor(theme, color))
	}
	for _, edge := range row.Edges {
		start := middle
		if row.Working && edge.From == row.Column {
			start += 3.6
		}
		path := new(ui.Path).MoveTo(x(edge.From), start)
		path.CubeTo(x(edge.From), middle+r.H/4, x(edge.To), middle+r.H/4, x(edge.To), r.Y+r.H)
		p.StrokePath(path, 1.6, historyColor(theme, edge.Color))
	}
	dot := new(ui.Path).Circle(x(row.Column), middle, 3.6)
	if row.Working {
		p.StrokePath(dot, 1.6, theme.TextMuted)
	} else {
		p.FillPath(dot, historyColor(theme, row.Color))
	}
}

func historyDate(value string) string {
	if date, err := time.Parse(time.RFC3339, value); err == nil {
		return date.Local().Format("2006-01-02 15:04")
	}
	return value
}

func historyRefs(refs string, theme *ui.Theme) []ui.Span {
	var spans []ui.Span
	for ref := range strings.SplitSeq(refs, ", ") {
		if ref == "" {
			continue
		}
		color := theme.Accent
		if strings.HasPrefix(ref, "tag: ") {
			color = theme.Warning
		} else if strings.Contains(ref, "/") && !strings.Contains(ref, "HEAD") {
			color = theme.TextMuted
		}
		if len(spans) > 0 {
			spans = append(spans, ui.Span{Text: " ", Size: 10})
		}
		spans = append(spans, ui.Span{
			Text: strings.TrimPrefix(ref, "tag: "), Size: 10, Weight: 600, Color: color,
			Background: color.Alpha(0.12), Radius: 3, PaddingX: 4, PaddingY: 1,
		})
	}
	return spans
}

func (app *nativeApp) historyWorkspace(c *ui.Context) {
	if app.repository == nil {
		return
	}
	ui.Column(c).Fill().MinHeight(0).Children(func() {
		app.operationBar(c)
		app.prefs.History = max(30, min(70, app.prefs.History))
		ui.SplitVerticalPercent(c, &app.prefs.History, func() {
			ui.Column(c).Fill().MinHeight(0).Children(func() { app.historyPanel(c) })
		}, func() {
			ui.Column(c).Fill().MinHeight(0).Children(func() {
				app.historyDetails(c)
				ui.Divider(c)
				ui.Split(c, &app.prefs.HistoryFiles, func() {
					if app.historyWorking {
						app.historyWorkingFiles(c)
					} else {
						app.historyFiles(c)
					}
				}, func() {
					app.diffPanel(c)
				}).FillWidth().Grow(1).MinHeight(0).SplitReset(280)
			})
		}).FillWidth().Grow(1).MinHeight(0).SplitReset(56)
	})
}

func (app *nativeApp) historyPanel(c *ui.Context) {
	if app.screen == "search" {
		app.searchControls(c)
	} else {
		ui.Row(c).FillWidth().Padding(4, 10).Gap(8).AlignItems(ui.Center).Children(func() {
			scopes := []string{locales.AllBranches(), locales.CurrentBranch()}
			scope := scopes[0]
			if app.historyFilter.CurrentBranch {
				scope = scopes[1]
			}
			c.Key("history-scope")
			ui.Select(c, &scope, scopes).Label(locales.HistoryBranchScope()).Width(140).Height(26).FontSize(12).
				Disabled(app.busy).OnChange(func() {
				app.historyFilter.CurrentBranch = scope == scopes[1]
				app.searchHistory(false)
			})
			remotes := []string{locales.ShowRemoteBranches(), locales.HideRemoteBranches()}
			remote := remotes[0]
			if app.historyFilter.HideRemotes {
				remote = remotes[1]
			}
			c.Key("history-remotes")
			ui.Select(c, &remote, remotes).Label(locales.HistoryRemoteVisibility()).Width(190).Height(26).FontSize(12).
				Disabled(app.busy).OnChange(func() {
				app.historyFilter.HideRemotes = remote == remotes[1]
				app.searchHistory(false)
			})
			app.historyReadControls(c)
		})
	}
	if app.viewedTag != "" {
		ui.Row(c).FillWidth().Padding(4, 10).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, nativeIcons["tag"]).FontSize(12).Shrink(0)
			ui.Text(c, app.viewedTag).FontSize(12).SingleLine().Tooltip(app.viewedTag).MinWidth(0).Shrink(1)
			if app.historySelected < 0 {
				ui.Text(c, locales.TagOutsideHistoryFilter()).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine().MinWidth(0).Shrink(1)
				ui.Button(c, locales.ViewInAllHistory()).FontSize(12).Disabled(app.busy || app.readTask != nil).
					OnClick(func() { app.selectTag(app.viewedTag, true) })
			}
		})
	}
	if app.historyOffset > 0 {
		ui.Button(c, locales.BackToLatestCommits()).FontSize(12).Disabled(app.busy || app.readTask != nil).
			OnClick(func() { app.searchHistory(false) })
	}
	ui.Divider(c)
	workingOffset := app.historyWorkingOffset()
	if len(app.history) == 0 && workingOffset == 0 {
		app.historyGraph, app.historyGraphSource, app.historyGraphCache = nil, nil, nil
		ui.Column(c).Key("empty-history").Grow(1).FillWidth().Center().Children(func() {
			message := locales.NoMatchingCommits()
			if app.screen == "search" && !app.historyLoaded {
				message = locales.SearchStartHint()
			}
			ui.Text(c, message).TextColor(c.Theme().TextMuted)
		})
		return
	}
	// 图谱、图谱列宽与引用列宽一起缓存，失效条件见 historyGraphCache；翻页时只续算新增部分。
	if app.historyGraphCache == nil {
		app.historyGraphCache = &historyGraphCache{}
	}
	cache := app.historyGraphCache
	app.historyGraph = cache.update(c, app.history, app.repository.snapshot.Head, workingOffset != 0)
	app.historyGraphSource = firstCommit(app.history)
	graphWidth := cache.width
	app.historyTableSelected = -1
	if app.historyWorking && workingOffset != 0 {
		app.historyTableSelected = 0
	} else if app.historySelected >= 0 {
		app.historyTableSelected = app.historySelected + workingOffset
	}
	app.historyList.Selected = &app.historyTableSelected
	history := app.history
	workingLabel := locales.UncommittedFiles(fmt.Sprint(len(app.repository.snapshot.Files)))
	app.historyList.Key = func(index int) any {
		if workingOffset != 0 && index == 0 {
			return "working-tree"
		}
		return history[index-workingOffset].Hash
	}
	app.historyList.Label = func(index int) string {
		if workingOffset != 0 && index == 0 {
			return workingLabel
		}
		return history[index-workingOffset].Subject
	}
	app.historyList.CellMenu = func(menu *ui.Menu, row, _ int) {
		row -= workingOffset
		if row < 0 || row >= len(history) {
			return
		}
		commit := history[row]
		if app.screen == "search" && menu.Item(locales.ViewInCommitHistory()).Disabled(app.busy || app.readTask != nil).Chosen() {
			app.revealSearchCommit(commit.Hash)
		}
		unavailable := !app.canCreateReference() || app.readTask != nil && app.readTask.kind == nativeReadHistory
		if menu.Item(locales.BranchFromCommit()).Disabled(unavailable).Chosen() {
			app.referenceDialog(false, locales.ReferenceCommit()+" "+commit.Subject, commit.Hash, false)
		}
		if menu.Item(locales.TagFromCommit()).Disabled(unavailable).Chosen() {
			app.referenceDialog(true, locales.ReferenceCommit()+" "+commit.Subject, commit.Hash, false)
		}
	}
	theme := *c.Theme()
	tableTheme := theme
	tableTheme.Spacing, tableTheme.FontSize = 3.5, 14
	tableTheme.Accent = theme.Accent.Alpha(0.16)
	tableTheme.InactiveAccent, tableTheme.AccentText = tableTheme.Accent, theme.Text
	ui.Themed(c, &tableTheme).Key(app.historyList).FillWidth().Grow(1).MinHeight(0).Children(func() {
		messageWidth := float32(200)
		if cache.refsWidth > 0 {
			messageWidth += cache.refsWidth + 4 + tableTheme.Space(5)
		}
		columns := []ui.TableColumn{
			{Title: locales.Graph(), Width: graphWidth, Fixed: true},
			{Title: locales.CommitMessage(), MinWidth: messageWidth},
			{Title: locales.Author(), Width: 110, MinWidth: 70},
			{Title: app.historyDateLabel(), Width: 138, MinWidth: 138},
			{Title: locales.CommitID(), Width: 88, MinWidth: 88},
		}
		label := locales.HistoryTable()
		searching := app.screen == "search"
		if searching {
			columns = columns[1:]
			label = locales.SearchResults()
		}
		if ui.Table(c, app.historyList, columns, len(app.history)+workingOffset, func(index, column int) {
			if searching {
				column++
			}
			working := workingOffset != 0 && index == 0
			var commit gitrepo.Commit
			if !working {
				commit = app.history[index-workingOffset]
			}
			switch column {
			case 0:
				ui.Box(c).Width(graphWidth - 17.5).MinHeight(17.5).AlignSelf(ui.Stretch).Draw(func(p *ui.Painter, r ui.Rect) {
					r.Y, r.H = r.Y-5.25, r.H+10.5
					drawHistoryGraph(p, r, app.historyGraph[index], &theme)
				})
			case 1:
				if working {
					ui.Text(c, workingLabel).Bold().SingleLine().Tooltip(workingLabel)
					return
				}
				ui.Row(c).FillWidth().MinWidth(0).Gap(4).AlignItems(ui.Center).Children(func() {
					if spans := historyRefs(commit.Refs, &theme); len(spans) > 0 {
						ui.RichText(c, spans...).Shrink(0).NoWrap().Tooltip(commit.Refs)
					}
					ui.Text(c, commit.Subject).Grow(1).MinWidth(0).SingleLine().Tooltip(commit.Subject)
				})
			case 2:
				ui.Text(c, commit.Author).FontSize(12).TextColor(theme.TextMuted).SingleLine()
			case 3:
				ui.Text(c, historyDate(app.commitDate(commit))).FontSize(12).TextColor(theme.TextMuted).SingleLine()
			case 4:
				ui.Text(c, shortHash(commit.Hash)).Font("monospace").FontSize(12).TextColor(theme.TextMuted).SingleLine()
			}
		}).Fill().Label(label).
			Disabled(app.busy || app.readTask != nil && app.readTask.kind == nativeReadHistory).
			Changed() && app.historyTableSelected >= 0 {
			if workingOffset != 0 && app.historyTableSelected == 0 {
				app.selectHistoryWorking()
				return
			}
			index := app.historyTableSelected - workingOffset
			if index >= len(app.history) {
				return
			}
			hash := app.history[index].Hash
			if app.historyWorking || hash != app.commitHash || app.readTask != nil || app.problem != "" {
				app.selectCommit(hash)
			}
		}
	})
	if app.screen == "search" {
		ui.Text(c, app.searchSummary()).FontSize(11).TextColor(c.Theme().TextMuted).Padding(4, 10)
	}
	if app.historyMore && app.historyPagePaused {
		ui.Button(c, locales.Retry()).Disabled(app.busy || app.readTask != nil).
			OnClick(func() { app.searchHistory(true) })
	}
	app.loadHistoryNearEnd(c)
}

func (app *nativeApp) historyDetails(c *ui.Context) {
	ui.Column(c).FillWidth().Padding(9, 14).Gap(5).Children(func() {
		if app.historyWorking {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.UncommittedChanges()).Bold().FontSize(15).Grow(1)
				ui.Button(c, locales.GoToFileChanges()).Disabled(app.busy || app.readTask != nil).OnClick(app.openHistoryWorkingChanges)
			})
			return
		}
		if app.commitHash == "" {
			ui.Text(c, locales.SelectCommit()).TextColor(c.Theme().TextMuted)
			return
		}
		var selected *gitrepo.Commit
		if app.viewedCommit.Hash == app.commitHash {
			selected = &app.viewedCommit
		}
		if commit := app.historyGraphCache.commit(app.history, app.commitHash); commit != nil {
			selected = commit
		}
		if selected != nil {
			ui.Text(c, selected.Subject).Bold().FontSize(15).MaxLines(1).Selectable().Tooltip(selected.Subject)
		}
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			if selected != nil {
				ui.Text(c, selected.Author).FontSize(11).MaxWidth(120).SingleLine().Selectable()
				ui.Text(c, historyDate(app.commitDate(*selected))).FontSize(11).TextColor(c.Theme().TextMuted).SingleLine().Tooltip(app.historyDateLabel())
			}
			ui.Text(c, app.commitHash).Font("monospace").FontSize(11).MinWidth(0).Grow(1).SingleLine().Selectable().Label(locales.FullCommitID())
			if app.screen == "search" {
				hash := app.commitHash
				ui.Button(c, locales.ViewInCommitHistory()).FontSize(12).Disabled(app.busy || app.readTask != nil).
					OnClick(func() { app.revealSearchCommit(hash) })
			}
		})
		if selected != nil && selected.Refs != "" {
			ui.Text(c, selected.Refs).FontSize(11).TextColor(c.Theme().TextMuted).MaxLines(1).Selectable().Tooltip(selected.Refs)
		}
	})
}

func (app *nativeApp) historyFiles(c *ui.Context) {
	empty := ""
	if app.commitHash != "" {
		empty = locales.EmptyCommit()
	}
	app.revisionFileList(c, app.commitFiles, app.commitList, &app.commitSelected, locales.CommitFileList(), empty, func(file string) {
		if file != app.commitFile || app.readTask != nil || app.problem != "" {
			app.selectCommitFile(file)
		}
	})
}

func (app *nativeApp) revisionFileList(c *ui.Context, files []gitrepo.CommitFile, list *ui.ListState, selected *int, label, empty string, choose func(string)) {
	theme := *c.Theme()
	theme.Accent = theme.Accent.Alpha(0.16)
	theme.InactiveAccent, theme.AccentText = theme.Accent, theme.Text
	panel := ui.Themed(c, &theme).Key(list).Fill().MinWidth(0)
	collapsed := ui.Local(panel, "revision-directories", func() map[string]bool { return make(map[string]bool) })
	cache := *ui.Local(panel, "revision-files", func() *nativeRevisionFiles { return &nativeRevisionFiles{} })
	outline := *ui.Local(panel, "revision-outline", newNativeFileOutline)
	panel.Children(func() {
		ui.Column(c).FillWidth().Padding(6, 8).Gap(2).Children(func() {
			ui.Text(c, locales.ChangedFiles(fmt.Sprint(len(files)))).FontSize(12).Bold()
			ui.Toolbar(c, func() {
				app.fileSortingControl(c)
				ui.Spacer(c)
				app.fileViewControl(c)
			}).Label(locales.FileDisplayOptions()).FillWidth().Padding(0)
		})
		ui.Divider(c)
		if len(files) == 0 {
			if empty != "" {
				ui.Text(c, empty).Padding(12).FontSize(12).TextColor(c.Theme().TextMuted)
			}
			return
		}
		path := ""
		if *selected >= 0 && *selected < len(files) {
			path = files[*selected].Path
		}
		// 选择索引与异步 Diff 回填共用原切片，排序时必须按路径恢复，避免索引指向其他文件。
		cache.sort(files, app.prefs.FileSort, app.prefs.FileDescending)
		*selected = -1
		if index, found := cache.indices[path]; found && path != "" {
			*selected = index
		} else {
			path = ""
		}
		disabled := app.readTask != nil && app.readTask.kind != nativeReadDiff
		if app.prefs.FileTree {
			app.revisionFileTree(c, cache.fileTree(files), outline, *collapsed, cache.indices, selected, path, label, disabled, choose)
			return
		}
		list.Selected = selected
		list.Key = func(index int) any { return files[index].Path }
		list.Label = func(index int) string { return files[index].Path }
		if ui.List(c, list, len(files), func(index int) {
			file := files[index]
			ui.Row(c).ContextMenu(app.filePathMenu(c, file.Path)).FillWidth().Height(30).Padding(0, 12).Gap(8).AlignItems(ui.Center).Children(func() {
				nativeFileIcon(c, file.Path)
				ui.Text(c, file.Path).FontSize(12).Grow(1).MinWidth(0).SingleLine().Tooltip(file.Path)
				ui.Text(c, file.Status).FontSize(10).TextColor(c.Theme().TextMuted)
			})
		}).FillWidth().Grow(1).MinHeight(0).Disabled(disabled).Label(label).Changed() && *selected >= 0 && *selected < len(files) {
			choose(files[*selected].Path)
		}
	})
}
