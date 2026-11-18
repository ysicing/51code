// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"cmp"
	"math"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func matchesNativeFileFilter(file gitrepo.File, filter string) bool {
	untracked := file.Index == "?" && file.Working == "?"
	switch filter {
	case "conflicts":
		return file.Conflict
	case "untracked":
		return untracked
	case "tracked":
		return !untracked
	default:
		return true
	}
}

func nativeFileStatus(file gitrepo.File, staged bool) string {
	if file.Conflict {
		return "!"
	}
	if staged {
		return file.Index
	}
	return file.Working
}

func compareNativeFiles(first, second gitrepo.File, order string, descending, staged bool) int {
	var result int
	switch order {
	case "name":
		result = cmp.Compare(filepath.Base(first.Path), filepath.Base(second.Path))
	case "status":
		result = cmp.Compare(nativeFileStatus(first, staged), nativeFileStatus(second, staged))
	}
	if result == 0 {
		result = cmp.Compare(first.Path, second.Path)
	}
	if descending {
		return -result
	}
	return result
}

func (app *nativeApp) filesFiltered() bool {
	// 历史工作区始终展示完整分组，不继承文件变更页中暂时隐藏的文件范围。
	if app.screen == "history" && app.historyWorking {
		return false
	}
	return app.fileQuery != "" || app.fileFilter != ""
}

func (app *nativeApp) fileControls(c *ui.Context) {
	ui.Row(c).FillWidth().Padding(8, 12, 4, 12).Gap(6).AlignItems(ui.Center).Children(func() {
		if ui.TextInput(c, &app.fileQuery).Label(locales.FileFilter()).Search().Placeholder(locales.FileFilter()).Grow(1).MinWidth(0).Changed() {
			clear(app.fileCollapsed)
		}
		if app.filesFiltered() {
			if iconButton(c, locales.ClearFilter(), nativeIcons["close"]).Clicked() {
				app.fileQuery, app.fileFilter = "", ""
				clear(app.fileCollapsed)
			}
		}
	})
	ui.Toolbar(c, func() {
		filters := []struct{ value, label string }{
			{"", locales.AllChanges()}, {"conflicts", locales.Conflicts()}, {"untracked", locales.Untracked()}, {"tracked", locales.TrackedChanges()},
		}
		filterLabel := locales.AllChanges()
		for _, filter := range filters {
			if app.fileFilter == filter.value {
				filterLabel = filter.label
			}
		}
		if app.fileFilter == "tracked" {
			filterLabel = locales.Tracked()
		}
		ui.MenuButton(c, filterLabel, func(menu *ui.Menu) {
			for _, filter := range filters {
				if menu.Item(filter.label).Checked(app.fileFilter == filter.value).Chosen() {
					app.fileFilter = filter.value
					clear(app.fileCollapsed)
				}
			}
		}).Label(locales.FileFiltering()).FontSize(12)
		app.fileSortingControl(c)
		ui.Spacer(c)
		app.fileViewControl(c)
	}).Label(locales.FileDisplayOptions()).FillWidth().Padding(0, 8, 4, 8)
}

func (app *nativeApp) fileSortingControl(c *ui.Context) {
	orderLabel := locales.Path()
	switch app.prefs.FileSort {
	case "name":
		orderLabel = locales.FileName()
	case "status":
		orderLabel = locales.Status()
	}
	if app.prefs.FileDescending {
		orderLabel += " ↓"
	}
	ui.MenuButton(c, orderLabel, func(menu *ui.Menu) {
		for _, order := range []struct{ value, label string }{
			{"path", locales.SortByPath()}, {"name", locales.SortByName()}, {"status", locales.SortByStatus()},
		} {
			if menu.Item(order.label).Checked(app.prefs.FileSort == order.value).Chosen() {
				app.prefs.FileSort = order.value
			}
		}
		menu.Separator()
		if menu.Item(locales.Descending()).Checked(app.prefs.FileDescending).Chosen() {
			app.prefs.FileDescending = !app.prefs.FileDescending
		}
	}).Label(locales.FileSorting()).FontSize(12)
}

func (app *nativeApp) fileViewControl(c *ui.Context) {
	viewLabel := locales.List()
	if app.prefs.FileTree {
		viewLabel = locales.Tree()
	}
	ui.MenuButton(c, viewLabel, func(menu *ui.Menu) {
		if menu.Item(locales.FlatList()).Checked(!app.prefs.FileTree).Chosen() {
			app.prefs.FileTree = false
		}
		if menu.Item(locales.Tree()).Checked(app.prefs.FileTree).Chosen() {
			app.prefs.FileTree = true
		}
	}).Label(locales.FileView()).FontSize(12)
}

type nativeFileNode struct {
	path     string
	file     *gitrepo.File
	children []*nativeFileNode
	// 以下字段由 newNativeFileTree 填充，随所属节点树一起失效。
	key         string
	collapseKey string
	depth       int
	childKeys   []string
	// width 是文件名宽度，按字体与缩放校验后复用。
	widthValid bool
	widthFont  string
	widthZoom  float32
	width      float32
}

// nativeFileMetrics 缓存文件面板的测量与派生数据；adopt 打开或刷新仓库时整体重置。
type nativeFileMetrics struct {
	source *repositoryView
	font   string
	zoom   float32
	widths map[string]float32
	// sections 依次为未暂存、已暂存分组，各自按 nativeFileSectionKey 校验。
	sections [2]nativeFileSection
}

func nativeFileScroll(element ui.Element) ui.Element {
	scroll := ui.Local(element, "file-scroll", func() ui.ScrollState { return ui.ScrollState{} })
	return element.TrackScroll(scroll).HandleInput(func(event ui.InputEvent) bool {
		if event.Kind != ui.InputScroll || !event.Precise || event.Mods&ui.Shift != 0 ||
			event.DY == 0 || math.Abs(float64(event.DX)) > math.Abs(float64(event.DY)) {
			return false
		}
		// 纵向手势到达边界后仍保持纵向，避免触摸板的微小横向分量挪动文件路径。
		scroll.Y = max(0, min(scroll.Y+event.DY, scroll.MaxY))
		return true
	})
}

func (app *nativeApp) filePathMenu(c *ui.Context, path string) func(*ui.Menu) {
	services := c.Services()
	root := ""
	if app.repository != nil {
		root = app.repository.snapshot.Path
	}
	return func(menu *ui.Menu) {
		if menu.Item(locales.CopyRelativePath()).Chosen() {
			services.WriteClipboard(path)
		}
		if menu.Item(locales.CopyFullPath()).Disabled(root == "").Chosen() {
			services.WriteClipboard(filepath.Join(root, filepath.FromSlash(path)))
		}
	}
}

func (app *nativeApp) filePathWidth(c *ui.Context, path string) float32 {
	metrics := &app.fileMetrics
	if metrics.source != app.repository || metrics.font != c.Theme().Font || metrics.zoom != c.Zoom() || metrics.widths == nil {
		// 宽度只复用当前快照及字体环境，不缓存文件状态或 Git 内容；分组缓存另有自己的键。
		metrics.source, metrics.font, metrics.zoom = app.repository, c.Theme().Font, c.Zoom()
		metrics.widths = make(map[string]float32)
	}
	if width, exists := metrics.widths[path]; exists {
		return width
	}
	width, _ := c.MeasureText(0, ui.Span{Text: path, Font: c.Theme().Font, Size: 12})
	metrics.widths[path] = width
	return width
}

func nativeFileNodes(files []gitrepo.File, order string, descending, staged bool) []*nativeFileNode {
	var roots []*nativeFileNode
	directories := make(map[string]*nativeFileNode)
	for _, file := range files {
		children := &roots
		parts := strings.Split(file.Path, "/")
		path := ""
		for _, part := range parts[:len(parts)-1] {
			path += part + "/"
			node := directories[path]
			if node == nil {
				node = &nativeFileNode{path: strings.TrimSuffix(path, "/")}
				directories[path] = node
				*children = append(*children, node)
			}
			children = &node.children
		}
		*children = append(*children, &nativeFileNode{path: file.Path, file: &file})
	}
	var sortNodes func([]*nativeFileNode)
	sortNodes = func(nodes []*nativeFileNode) {
		slices.SortFunc(nodes, func(first, second *nativeFileNode) int {
			if (first.file == nil) != (second.file == nil) {
				if first.file == nil {
					return -1
				}
				return 1
			}
			if first.file != nil {
				return compareNativeFiles(*first.file, *second.file, order, descending, staged)
			}
			result := cmp.Compare(first.path, second.path)
			if descending {
				return -result
			}
			return result
		})
		for _, node := range nodes {
			sortNodes(node.children)
		}
	}
	sortNodes(roots)
	return roots
}

// changeFileCheckbox 构建勾选框；勾选键与 nativeSelectionKey 一致。
func (app *nativeApp) changeFileCheckbox(c *ui.Context, file gitrepo.File, staged bool) {
	label := locales.SelectUnstagedFile(file.Path)
	if staged {
		label = locales.SelectStagedFile(file.Path)
	}
	key := nativeSelectionKey(staged, file.Path)
	checked := app.selectedFiles[key]
	if ui.Checkbox(c, &checked, "").Label(label).Disabled(file.Conflict || !app.writable || app.readTask != nil).Changed() {
		if checked {
			app.selectedFiles[key] = true
		} else {
			delete(app.selectedFiles, key)
		}
	}
}

// changeFileContent 填充文件行的状态、图标与名称；平面列表与目录树共用。
func (app *nativeApp) changeFileContent(c *ui.Context, row ui.Element, file gitrepo.File, staged bool, name string, theme *ui.Theme) {
	status := nativeFileStatus(file, staged)
	color := theme.Warning
	switch status {
	case "A", "?":
		color = theme.Success
	case "D", "!":
		color = theme.Danger
	}
	row.ContextMenu(app.filePathMenu(c, file.Path))
	row.Grow(1).Height(28).Padding(4, 6).Gap(6).AlignItems(ui.Center).Tooltip(file.Path).Children(func() {
		ui.Text(c, status).Width(12).Font("monospace").FontSize(11).TextColor(color)
		nativeFileIcon(c, file.Path)
		ui.Text(c, name).FontSize(12).Grow(1).MinWidth(0).SingleLine()
	})
}

func (app *nativeApp) chooseChangeFile(file gitrepo.File, staged bool) {
	if app.screen == "history" && app.historyWorking {
		app.selectHistoryWorkingFile(file.Path, staged)
	} else {
		app.selectFile(file.Path, staged)
	}
}

func (app *nativeApp) changeFileSelected(file gitrepo.File, staged bool) bool {
	return app.selectedFile == file.Path && app.staged == staged
}

// changeFileRow 构建平面列表中的文件行。
func (app *nativeApp) changeFileRow(c *ui.Context, file gitrepo.File, staged bool) {
	ui.Row(c).Key("file:" + nativeSelectionKey(staged, file.Path)).FillWidth().Gap(2).AlignItems(ui.Center).Children(func() {
		app.changeFileCheckbox(c, file, staged)
		selected := app.changeFileSelected(file, staged)
		row := listRow(c, file.Path, selected)
		app.changeFileContent(c, row, file, staged, file.Path, c.Theme())
		if row.DoubleClicked() && app.prefs.DoubleClickStage {
			app.stageDoubleClickedFile(file, staged)
		} else if row.Clicked() && (!selected || app.readTask != nil || app.problem != "") {
			app.chooseChangeFile(file, staged)
		}
	})
}

// changesFileTree 用 Outline 只构建可见行；展开状态仍记录在 app.fileCollapsed。
//
// 交互与原 TreeBase 目录树的差异：方向键移动光标即选中文件并读取 Diff（与平面列表一致），
// 左右键展开、折叠或跳到父目录；鼠标点击目录只展开或折叠，不改变选中文件。
func (app *nativeApp) changesFileTree(c *ui.Context, outline *nativeFileOutline, tree *nativeFileTree, staged bool, label string, width float32) {
	selected := ""
	if app.selectedFile != "" && app.staged == staged {
		selected = "f:" + app.selectedFile
	}
	outline.sync(tree, app.fileCollapsed, selected)
	theme := *c.Theme()
	// Outline 自带的选中与悬停底色会盖住勾选框；改为在行内容上按原目录树样式绘制，
	// 勾选框再套回原主题，保持强调色。
	quiet := theme
	quiet.Accent, quiet.InactiveAccent, quiet.SurfaceHover, quiet.AccentText = ui.Transparent, ui.Transparent, ui.Transparent, theme.Text
	outline.state.List.Label = func(row int) string {
		node := tree.nodes[outline.state.Item(row)]
		switch {
		case node.file != nil:
			return node.path
		case staged:
			return locales.StagedDirectory(node.path)
		default:
			return locales.UnstagedDirectory(node.path)
		}
	}
	handled := false
	choose := func(file gitrepo.File) {
		app.chooseChangeFile(file, staged)
		if app.conflictDirty() {
			outline.pinned = false
			outline.cursor = outline.row(selected)
		}
	}
	ui.Themed(c, &quiet).Label(locales.SectionList(label)).Grow(1).MinHeight(0).FillWidth().Children(func() {
		list := ui.Outline(c, &outline.state, tree.roots, tree.children, func(key string) {
			node := tree.nodes[key]
			active := node == outline.chosen()
			if node.file == nil {
				if app.directoryRow(c, node, outline.state.Open.Has(key), active, 28, 6, 6, &theme) {
					outline.toggle(node, app.fileCollapsed)
					handled = true
					// 展开状态在本遍 Outline 展平之后才改变，需要再构建一遍。
					c.Invalidate()
				}
				return
			}
			file := *node.file
			ui.Row(c).FillWidth().PaddingStart(float32(node.depth) * 16).Gap(2).AlignItems(ui.Center).Children(func() {
				ui.Themed(c, &theme).Shrink(0).Children(func() { app.changeFileCheckbox(c, file, staged) })
				row := ui.Row(c).Radius(5)
				if active {
					row.Background(theme.Accent.Alpha(0.12))
				} else if row.Hovered() {
					row.Background(theme.SurfaceHover)
				}
				app.changeFileContent(c, row, file, staged, filepath.Base(file.Path), &theme)
				if row.RowDoubleClicked() && app.prefs.DoubleClickStage {
					handled = true
					app.stageDoubleClickedFile(file, staged)
				} else if row.RowClicked() {
					handled = true
					outline.pinned = true
					if !app.changeFileSelected(file, staged) || app.readTask != nil || app.problem != "" {
						choose(file)
					}
				}
			})
		})
		list = nativeFileScroll(list.Label(locales.SectionTree(label)).Grow(1).MinHeight(0).FillWidth().Padding(0, 6, 6).RowWidth(width))
		// 键盘移动光标到文件时选中它；鼠标点击已在行内处理，避免同一帧重复读取。
		if list.Changed() && !handled {
			if node := outline.keyboard(); node != nil && !app.changeFileSelected(*node.file, staged) {
				choose(*node.file)
			}
		}
	})
}

// directoryRow 构建目录行并报告点击。点击仍交给 Outline 的行（辅助功能与 Tester 以行为目标），
// 光标随后按 nativeFileOutline.sync 的规则回到选中文件。theme 为 nil 时选中与悬停底色交给 Outline 绘制。
func (app *nativeApp) directoryRow(c *ui.Context, node *nativeFileNode, open, active bool, height, padding, gap float32, theme *ui.Theme) bool {
	row := ui.Row(c).Tooltip(node.path).Height(height).FillWidth().Padding(0, padding).
		PaddingStart(padding + float32(node.depth)*16).Gap(gap).AlignItems(ui.Center).Radius(5)
	muted := c.Theme().TextMuted
	if theme != nil {
		muted = theme.TextMuted
		if active {
			row.Background(theme.Accent.Alpha(0.12))
		} else if row.Hovered() {
			row.Background(theme.SurfaceHover)
		}
	}
	row.ContextMenu(app.filePathMenu(c, node.path))
	row.Children(func() {
		nativeDisclosure(c, open, muted)
		ui.Icon(c, nativeIcons["folder"]).FontSize(15).TextColor(muted)
		ui.Text(c, filepath.Base(node.path)).FontSize(12).Grow(1).MinWidth(0).SingleLine()
	})
	return row.RowClicked()
}
