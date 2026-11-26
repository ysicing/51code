// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"path/filepath"
	"slices"

	"github.com/ysicing/51code/internal/gitrepo"

	"github.com/egoist/mygo/ui"
)

// nativeRevisionFiles 缓存提交或比较文件列表的排序、路径索引与目录树，挂在列表面板的 ui.Local 上。
//
// 失效条件：files 首元素地址或长度变化（换提交、换比较结果、调用方替换切片），
// 或排序字段、方向变化。排序原地改写 files，键不变时说明切片已按当前设置排好，不必再排。
type nativeRevisionFiles struct {
	valid      bool
	files      *gitrepo.CommitFile
	count      int
	order      string
	descending bool
	indices    map[string]int
	tree       *nativeFileTree
}

func (cache *nativeRevisionFiles) sort(files []gitrepo.CommitFile, order string, descending bool) {
	var first *gitrepo.CommitFile
	if len(files) > 0 {
		first = &files[0]
	}
	if cache.valid && cache.files == first && cache.count == len(files) && cache.order == order && cache.descending == descending {
		return
	}
	slices.SortFunc(files, func(first, second gitrepo.CommitFile) int {
		return compareNativeFiles(
			gitrepo.File{Path: first.Path, Index: first.Status},
			gitrepo.File{Path: second.Path, Index: second.Status},
			order, descending, true,
		)
	})
	*cache = nativeRevisionFiles{valid: true, files: first, count: len(files), order: order, descending: descending,
		indices: make(map[string]int, len(files))}
	for index, file := range files {
		cache.indices[file.Path] = index
	}
}

// fileTree 懒建目录树；files 必须是刚经过 sort 的同一切片。
func (cache *nativeRevisionFiles) fileTree(files []gitrepo.CommitFile) *nativeFileTree {
	if cache.tree == nil {
		entries := make([]gitrepo.File, len(files))
		for index, file := range files {
			entries[index] = gitrepo.File{Path: file.Path, Index: file.Status}
		}
		cache.tree = newNativeFileTree(entries, cache.order, cache.descending, true, func(path string) string { return path })
	}
	return cache.tree
}

// revisionFileTree 用 Outline 只构建可见行。选中底色与悬停由 Outline 按面板主题绘制，
// 方向键移动即选中文件（与平面列表一致）；点击目录展开或折叠，选中文件可见时高亮留在文件上。
//
// current 是当前选中文件路径，未选中时为空。
func (app *nativeApp) revisionFileTree(c *ui.Context, tree *nativeFileTree, outline *nativeFileOutline, collapsed map[string]bool, indices map[string]int, selected *int, current, label string, disabled bool, choose func(string)) {
	if current != "" {
		current = "f:" + current
	}
	outline.sync(tree, collapsed, current)
	outline.state.List.Label = func(row int) string {
		node := tree.nodes[outline.state.Item(row)]
		if node.file == nil {
			return node.path + "/"
		}
		return node.path
	}
	pick := func(path string) {
		index := indices[path]
		if index != *selected || app.readTask != nil || app.problem != "" {
			*selected = index
			choose(path)
		}
	}
	handled := false
	list := ui.Outline(c, &outline.state, tree.roots, tree.children, func(key string) {
		node := tree.nodes[key]
		if node.file == nil {
			if app.directoryRow(c, node, outline.state.Open.Has(key), false, 30, 12, 8, nil) {
				outline.toggle(node, collapsed)
				handled = true
				// 展开状态在本遍 Outline 展平之后才改变，需要再构建一遍。
				c.Invalidate()
			}
			return
		}
		row := ui.Row(c).Tooltip(node.path).Height(30).FillWidth().Padding(0, 12).
			PaddingStart(12 + float32(node.depth)*16).Gap(8).AlignItems(ui.Center)
		row.ContextMenu(app.filePathMenu(c, node.path))
		row.Children(func() {
			nativeFileIcon(c, node.path)
			ui.Text(c, filepath.Base(node.path)).FontSize(12).Grow(1).MinWidth(0).SingleLine()
			app.reviewedFileBadge(c, node.path)
			ui.Text(c, node.file.Index).FontSize(10).TextColor(c.Theme().TextMuted)
		})
		if row.RowClicked() {
			handled = true
			outline.pinned = true
			pick(node.path)
		}
	})
	list = nativeFileScroll(list.FillWidth().Grow(1).MinHeight(0).Disabled(disabled).Label(label))
	// 键盘移动光标到文件时选中它；鼠标点击已在行内处理，避免同一帧重复读取。
	if list.Changed() && !handled {
		if node := outline.keyboard(); node != nil && indices[node.path] != *selected {
			pick(node.path)
		}
	}
}
