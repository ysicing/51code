// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"

	"github.com/egoist/mygo/ui"
)

// nativeFileSectionKey 标识文件变更面板某个分组的过滤结果。
//
// 刷新会整体替换 repository；测试和少数路径会原地替换或追加 snapshot.Files，
// 因此除 repository 指针外还比较切片首元素地址与长度。前提是生产代码不原地改写快照元素
// （已审计）；测试需要改文件状态时也要换新切片，而不是原地改元素。过滤词、状态筛选以及
// “是否处于过滤状态”（历史工作区忽略过滤）任一变化都会重新过滤。
type nativeFileSectionKey struct {
	repository *repositoryView
	files      *gitrepo.File
	count      int
	staged     bool
	filtered   bool
	query      string
	filter     string
}

// nativeFileSection 缓存一个分组每遍构建都要用到的派生数据，避免重复过滤、排序、建树和测量。
// 勾选集合与目录折叠状态会被多处直接改写，不进入缓存，每遍按这里预先拼好的键查表。
type nativeFileSection struct {
	valid bool
	key   nativeFileSectionKey
	// visible 保持快照顺序，供批量暂存使用；调用方只读，不能排序或修改。
	visible []gitrepo.File
	// keys 与 visible 一一对应，是 selectedFiles 使用的勾选键。
	keys []string
	// total 是未过滤时本分组的文件数，actionable 是可见文件中非冲突文件数。
	total, actionable int
	sorted            nativeFileSorted
	// widthFont 与 widthZoom 记录 pathWidth 的测量环境，字体或缩放变化时重新测量。
	widthValid bool
	widthFont  string
	widthZoom  float32
	pathWidth  float32
}

// nativeFileSorted 缓存按排序设置排列的文件与目录树；排序字段或方向变化时重建。
type nativeFileSorted struct {
	valid      bool
	order      string
	descending bool
	files      []gitrepo.File
	tree       *nativeFileTree
}

func nativeSelectionKey(staged bool, path string) string {
	return strconv.FormatBool(staged) + ":" + path
}

func firstFile(files []gitrepo.File) *gitrepo.File {
	if len(files) == 0 {
		return nil
	}
	return &files[0]
}

// fileSection 返回当前分组的缓存，必要时重新过滤。不依赖界面上下文，批量操作也可调用。
func (app *nativeApp) fileSection(staged bool) *nativeFileSection {
	section := &app.fileMetrics.sections[0]
	if staged {
		section = &app.fileMetrics.sections[1]
	}
	key := nativeFileSectionKey{repository: app.repository, staged: staged}
	if app.repository != nil {
		files := app.repository.snapshot.Files
		key.files, key.count = firstFile(files), len(files)
		key.filtered = app.filesFiltered()
		if key.filtered {
			key.query, key.filter = app.fileQuery, app.fileFilter
		}
	}
	if section.valid && section.key == key {
		return section
	}
	*section = nativeFileSection{valid: true, key: key}
	if app.repository == nil {
		return section
	}
	query := strings.ToLower(key.query)
	for _, file := range app.repository.snapshot.Files {
		if staged && !isStaged(file) || !staged && !isWorking(file) {
			continue
		}
		section.total++
		// 先比较廉价的状态筛选，再对路径做大小写折叠。
		if key.filtered && (!matchesNativeFileFilter(file, key.filter) ||
			query != "" && !strings.Contains(strings.ToLower(file.Path), query)) {
			continue
		}
		section.visible = append(section.visible, file)
		section.keys = append(section.keys, nativeSelectionKey(staged, file.Path))
		if !file.Conflict {
			section.actionable++
		}
	}
	return section
}

// sortedFiles 返回按当前排序设置排列的可见文件；结果只读。
func (section *nativeFileSection) sortedFiles(order string, descending bool) *nativeFileSorted {
	sorted := &section.sorted
	if sorted.valid && sorted.order == order && sorted.descending == descending {
		return sorted
	}
	*sorted = nativeFileSorted{valid: true, order: order, descending: descending, files: slices.Clone(section.visible)}
	staged := section.key.staged
	slices.SortFunc(sorted.files, func(first, second gitrepo.File) int {
		return compareNativeFiles(first, second, order, descending, staged)
	})
	return sorted
}

// fileTree 懒建目录树，只在目录树视图中使用。
func (sorted *nativeFileSorted) fileTree(staged bool) *nativeFileTree {
	if sorted.tree == nil {
		sorted.tree = newNativeFileTree(sorted.files, sorted.order, sorted.descending, staged, func(path string) string {
			return nativeSelectionKey(staged, path)
		})
	}
	return sorted.tree
}

// maxPathWidth 返回平面列表的最长路径宽度，字体或缩放变化时重新测量。
func (app *nativeApp) maxPathWidth(c *ui.Context, section *nativeFileSection) float32 {
	font, zoom := c.Theme().Font, c.Zoom()
	if section.widthValid && section.widthFont == font && section.widthZoom == zoom {
		return section.pathWidth
	}
	section.widthValid, section.widthFont, section.widthZoom, section.pathWidth = true, font, zoom, 0
	for _, file := range section.visible {
		section.pathWidth = max(section.pathWidth, app.filePathWidth(c, file.Path))
	}
	return section.pathWidth
}

// nativeFileTree 是排序后的目录树及 Outline 需要的按键索引。
// 键以 "d:"/"f:" 区分目录与文件，避免已删除文件与同名新目录冲突。
type nativeFileTree struct {
	roots []string
	nodes map[string]*nativeFileNode
	// dirs 按先序列出全部目录，供同步展开状态。
	dirs []*nativeFileNode
}

// newNativeFileTree 建树并为节点生成 Outline 键；collapseKey 生成目录在折叠表中的键。
func newNativeFileTree(files []gitrepo.File, order string, descending, staged bool, collapseKey func(string) string) *nativeFileTree {
	tree := &nativeFileTree{nodes: make(map[string]*nativeFileNode)}
	var index func(nodes []*nativeFileNode, depth int) []string
	index = func(nodes []*nativeFileNode, depth int) []string {
		keys := make([]string, len(nodes))
		for position, node := range nodes {
			node.depth = depth
			if node.file != nil {
				node.key = "f:" + node.path
			} else {
				node.key, node.collapseKey = "d:"+node.path, collapseKey(node.path)
				tree.dirs = append(tree.dirs, node)
				node.childKeys = index(node.children, depth+1)
			}
			tree.nodes[node.key] = node
			keys[position] = node.key
		}
		return keys
	}
	tree.roots = index(nativeFileNodes(files, order, descending, staged), 0)
	return tree
}

// children 供 Outline 使用：目录返回子节点键，文件返回 nil。
func (tree *nativeFileTree) children(key string) []string {
	if node := tree.nodes[key]; node != nil && node.file == nil {
		return node.childKeys
	}
	return nil
}

// visit 按展示顺序遍历未被折叠隐藏的节点；fn 返回 false 时停止。
func (tree *nativeFileTree) visit(collapsed func(*nativeFileNode) bool, fn func(*nativeFileNode) bool) {
	var walk func(keys []string) bool
	walk = func(keys []string) bool {
		for _, key := range keys {
			node := tree.nodes[key]
			if !fn(node) {
				return false
			}
			if node.file == nil && !collapsed(node) && !walk(node.childKeys) {
				return false
			}
		}
		return true
	}
	walk(tree.roots)
}

// size 返回展开后的行数和最宽行宽度；名称宽度按字体与缩放缓存在节点上。
func (app *nativeApp) fileTreeRows(c *ui.Context, tree *nativeFileTree, collapsed func(*nativeFileNode) bool) (int, float32) {
	count, width := 0, float32(0)
	tree.visit(collapsed, func(node *nativeFileNode) bool {
		count++
		width = max(width, app.fileNameWidth(c, node)+float32(node.depth)*16+88)
		return true
	})
	return count, width
}

func (app *nativeApp) fileNameWidth(c *ui.Context, node *nativeFileNode) float32 {
	font, zoom := c.Theme().Font, c.Zoom()
	if !node.widthValid || node.widthFont != font || node.widthZoom != zoom {
		node.width, _ = c.MeasureText(0, ui.Span{Text: filepath.Base(node.path), Font: font, Size: 12})
		node.widthValid, node.widthFont, node.widthZoom = true, font, zoom
	}
	return node.width
}

// nativeFileOutline 保存一个目录树 Outline 的界面状态，挂在所属面板的 ui.Local 上，
// 刷新快照时保留滚动位置与键盘光标。
//
// 目录展开状态的权威来源是调用方的折叠表（变更面板为 app.fileCollapsed，筛选变化时会被清空）；
// Outline 自己用方向键展开或折叠时只改 state.Open，下一遍构建通过与 open（上一遍写入的值）
// 比较识别出来，再写回折叠表。
//
// 光标（Outline 的选中行）规则，尽量保持原目录树“点击目录不改变选中文件”的观感：
//   - 外部选中文件变化时，光标移到该文件（不可见则为 -1）；
//   - 用户用键盘移动光标或点击文件后 pinned，光标以用户为准，直到外部选择再次变化；
//   - 未 pinned 时，选中文件可见则光标回到它；不可见（例如刚折叠其所在目录）则保留光标，
//     以便继续用左右键展开、折叠该目录。
type nativeFileOutline struct {
	state  ui.OutlineState[string]
	cursor int
	tree   *nativeFileTree
	open   map[string]bool
	// selected 是上一遍外部选中文件的 Outline 键。
	selected string
	pinned   bool
}

func newNativeFileOutline() *nativeFileOutline {
	outline := &nativeFileOutline{cursor: -1}
	// 自定义行首：缩进与展开箭头由行内容绘制，保持原目录树外观。
	outline.state.Prefix = func(ui.OutlineRow) {}
	outline.state.List.Selected = &outline.cursor
	return outline
}

// sync 合并 Outline 内部的展开变化与折叠表，并按上面的规则定位光标。
// selected 为空表示本树没有选中文件。
func (outline *nativeFileOutline) sync(tree *nativeFileTree, collapsed map[string]bool, selected string) {
	if outline.tree != tree {
		// 节点树重建后旧键可能不存在，重新开始记录；Open 清空后按折叠表重新写入。
		outline.tree, outline.open = tree, make(map[string]bool, len(tree.dirs))
		outline.state.Open.Clear()
	}
	for _, dir := range tree.dirs {
		open := !collapsed[dir.collapseKey]
		if previous, known := outline.open[dir.key]; known && previous != outline.state.Open.Has(dir.key) {
			open = outline.state.Open.Has(dir.key)
			collapsed[dir.collapseKey] = !open
		}
		if open {
			outline.state.Open.Add(dir.key)
		} else {
			outline.state.Open.Remove(dir.key)
		}
		outline.open[dir.key] = open
	}
	switch {
	case selected != outline.selected:
		outline.selected, outline.pinned = selected, false
		outline.cursor = outline.row(selected)
	case outline.pinned || selected == "":
	case outline.cursor >= 0 && outline.cursor < outline.state.Rows() && outline.state.Item(outline.cursor) == selected:
		// 上一遍光标已在选中文件上；行序变化由 List 按键跟随。
	default:
		if row := outline.row(selected); row >= 0 {
			outline.cursor = row
		}
	}
}

// row 返回 key 在当前展开状态下的行号，不可见时返回 -1。
func (outline *nativeFileOutline) row(key string) int {
	if key == "" || outline.tree == nil {
		return -1
	}
	found, row := -1, 0
	outline.tree.visit(func(node *nativeFileNode) bool { return !outline.state.Open.Has(node.key) }, func(node *nativeFileNode) bool {
		if node.key == key {
			found = row
			return false
		}
		row++
		return true
	})
	return found
}

// toggle 展开或折叠目录，同时写回折叠表。鼠标点击目录不算用户放置光标，
// 光标稍后按 sync 的规则回到选中文件。
func (outline *nativeFileOutline) toggle(dir *nativeFileNode, collapsed map[string]bool) {
	open := !outline.state.Open.Has(dir.key)
	if open {
		outline.state.Open.Add(dir.key)
	} else {
		outline.state.Open.Remove(dir.key)
	}
	outline.open[dir.key] = open
	collapsed[dir.collapseKey] = !open
	outline.pinned = false
}

// keyboard 处理没有伴随行点击的光标变化（方向键、Home/End）：光标停在目录上时 pinned，
// 停在文件上时返回该节点供调用方选中。
func (outline *nativeFileOutline) keyboard() *nativeFileNode {
	node := outline.chosen()
	if node == nil {
		return nil
	}
	outline.pinned = true
	if node.file == nil {
		return nil
	}
	return node
}

// chosen 返回光标所在节点；光标无效时返回 nil。
func (outline *nativeFileOutline) chosen() *nativeFileNode {
	if outline.tree == nil || outline.cursor < 0 || outline.cursor >= outline.state.Rows() {
		return nil
	}
	return outline.tree.nodes[outline.state.Item(outline.cursor)]
}
