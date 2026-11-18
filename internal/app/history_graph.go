// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"slices"

	"github.com/ysicing/51code/internal/gitrepo"

	"github.com/egoist/mygo/ui"
)

// historyGraphCheckpoint 是处理第 row 行之前的轨道状态，用于翻页后从中间重算。
type historyGraphCheckpoint struct {
	row       int
	lanes     []historyLane
	nextColor int
}

// historyGraphCheckpointEvery 控制检查点间隔：越小回退越精确，内存占用越多（每个检查点 O(轨道数)）。
const historyGraphCheckpointEvery = 256

// historyGraphBuilder 增量计算提交图，结果与对全部提交一次性调用 commitGraph 完全一致。
//
// 一行的结果只取决于此前的轨道状态与“父提交是否在列表中且位于下方”。翻页追加提交时，
// 旧行中曾因父提交缺失而跳过的父提交如果出现在新页，这些行及其后的结果都会改变；
// missing 记录每个缺失父提交最早被跳过的行，追加时据此找到最早受影响的行，
// 从不晚于它的检查点恢复轨道状态后重算。新页包含已有哈希（位置表被覆盖）时整表重算。
type historyGraphBuilder struct {
	hashes      []string
	parents     [][]string
	positions   map[string]int
	missing     map[string]int
	rows        []historyGraphRow
	lanes       []historyLane
	nextColor   int
	checkpoints []historyGraphCheckpoint
	// afterIndex 每行复用，记录处理后轨道的哈希到列号，避免逐轨道线性查找。
	afterIndex map[string]int
}

func newHistoryGraphBuilder() *historyGraphBuilder {
	return &historyGraphBuilder{positions: map[string]int{}, missing: map[string]int{}, afterIndex: map[string]int{}}
}

// append 追加提交，返回全部行及第一条重算的行号（小于追加前行数说明发生了回退）。
// 返回的切片在下次 append 前有效。
func (builder *historyGraphBuilder) append(commits []gitrepo.Commit) ([]historyGraphRow, int) {
	start := len(builder.hashes)
	from := start
	for index, commit := range commits {
		if _, exists := builder.positions[commit.Hash]; exists {
			// 重复哈希会改变已有提交的位置判断，无法局部重算。
			from = 0
		}
		builder.positions[commit.Hash] = start + index
		builder.hashes = append(builder.hashes, commit.Hash)
		builder.parents = append(builder.parents, commit.Parents)
	}
	for _, commit := range commits {
		if row, exists := builder.missing[commit.Hash]; exists {
			from = min(from, row)
		}
	}
	if from < start {
		builder.rewind(from)
	}
	first := len(builder.rows)
	for index := len(builder.rows); index < len(builder.hashes); index++ {
		builder.process(index)
	}
	return builder.rows, first
}

// rewind 恢复到不晚于 row 的检查点，丢弃其后的结果与缺失记录。
func (builder *historyGraphBuilder) rewind(row int) {
	position, _ := slices.BinarySearchFunc(builder.checkpoints, row+1, func(checkpoint historyGraphCheckpoint, target int) int {
		return checkpoint.row - target
	})
	// position 指向第一个 row > 目标的检查点；第 0 行总有检查点，所以 position ≥ 1。
	checkpoint := builder.checkpoints[position-1]
	builder.checkpoints = builder.checkpoints[:position]
	// 截断后收紧容量，使后续 append 重新分配：之前返回的行切片可能仍被
	// nativeHistoryState 的按值副本引用，不能原地覆盖。
	builder.rows = slices.Clip(builder.rows[:checkpoint.row])
	builder.lanes, builder.nextColor = slices.Clone(checkpoint.lanes), checkpoint.nextColor
	for hash, skipped := range builder.missing {
		if skipped >= checkpoint.row {
			delete(builder.missing, hash)
		}
	}
}

// process 计算第 index 行，算法与原 commitGraph 相同：
// 轨道颜色随父提交传递，不随列压缩改变；筛选掉的提交不能被画成直接相连。
func (builder *historyGraphBuilder) process(index int) {
	if index%historyGraphCheckpointEvery == 0 {
		builder.checkpoints = append(builder.checkpoints, historyGraphCheckpoint{
			row: index, lanes: slices.Clone(builder.lanes), nextColor: builder.nextColor,
		})
	}
	hash, parents := builder.hashes[index], builder.parents[index]
	lanes := builder.lanes
	var row historyGraphRow
	for _, lane := range lanes {
		row.Incoming = append(row.Incoming, lane.Color)
	}
	column := slices.IndexFunc(lanes, func(lane historyLane) bool { return lane.Hash == hash })
	if column < 0 {
		column = len(lanes)
		lanes = append(lanes, historyLane{Hash: hash, Color: builder.nextColor})
		builder.nextColor++
	}
	row.Column, row.Color = column, lanes[column].Color
	after := slices.Clone(lanes)
	after = slices.Delete(after, column, column+1)
	for parentIndex, parent := range parents {
		position, ok := builder.positions[parent]
		if !ok {
			if _, recorded := builder.missing[parent]; !recorded {
				builder.missing[parent] = index
			}
			continue
		}
		if position <= index {
			continue
		}
		// 父提交通常只有一两个，这里线性查找不影响整体复杂度。
		target := slices.IndexFunc(after, func(lane historyLane) bool { return lane.Hash == parent })
		if target >= 0 {
			if parentIndex == 0 {
				after[target].Color = row.Color
			}
			continue
		}
		lane := historyLane{Hash: parent, Color: row.Color}
		if parentIndex == 0 {
			after = slices.Insert(after, min(column, len(after)), lane)
		} else {
			lane.Color = builder.nextColor
			builder.nextColor++
			after = append(after, lane)
		}
	}
	afterIndex := builder.afterIndex
	clear(afterIndex)
	for position, lane := range after {
		// 与线性查找一致：同一哈希取第一条轨道。
		if _, exists := afterIndex[lane.Hash]; !exists {
			afterIndex[lane.Hash] = position
		}
	}
	for from, lane := range lanes {
		if from != column {
			if to, ok := afterIndex[lane.Hash]; ok {
				row.Edges = append(row.Edges, historyEdge{From: from, To: to, Color: after[to].Color})
			}
			continue
		}
		for _, parent := range parents {
			if to, ok := afterIndex[parent]; ok {
				row.Edges = append(row.Edges, historyEdge{From: from, To: to, Color: after[to].Color})
			}
		}
	}
	row.Columns = max(len(lanes), len(after))
	builder.lanes = after
	builder.rows = append(builder.rows, row)
}

// historyGraphCache 缓存历史表格的提交图及由其派生、原本每遍构建都要线性扫描的数据：
// 图谱列宽、引用列宽和哈希到行号的索引。挂在 nativeHistoryState 上，随浏览/搜索状态一起切换。
//
// 命中条件：history 首元素地址、长度、HEAD、是否显示未提交行、字体与缩放都不变。
// 未命中时，若旧结果的每个提交（哈希与父提交）仍是新列表的前缀，说明是翻页追加，
// 交给 historyGraphBuilder 续算；否则（换搜索条件、刷新改写历史、HEAD 变化等）整表重算。
// 引用文本可能在提交不变时改变（刷新后新增分支），所以前缀引用不一致时重新测量全部引用宽度。
//
// 前提：生产代码不会原地改写 history 的元素（翻页只追加，搜索与刷新整体替换），
// 因此首元素地址 + 长度足以识别同一份历史；测试修改历史时也必须换新切片。
//
// 共享与线程：nativeHistoryState 会按值拷贝（浏览/搜索切换、焦点刷新），副本持有同一个缓存指针。
// 缓存每次都用调用方传入的 history 校验键，切回的副本若历史不同会整表重算，不会用到另一份历史的图；
// 内部的 builder、map 与切片只是这一指针的私有派生数据，仅在主线程构建界面时读写，
// 后台 goroutine（refresh 读取的副本）不得访问。
type historyGraphCache struct {
	source  *gitrepo.Commit
	count   int
	head    string
	working bool
	font    string
	zoom    float32
	builder *historyGraphBuilder
	rows    []historyGraphRow
	// width 是图谱列宽，refsWidth 是最宽的引用标签；refs 与已测量的提交一一对应。
	width     float32
	refs      []string
	refsWidth float32
	// index 记录哈希第一次出现的行号（不含未提交行）。
	index map[string]int
}

// historyWorkingCommit 是图谱顶端“未提交的更改”行的占位提交，父提交为 HEAD。
func historyWorkingCommit(head string) gitrepo.Commit {
	return gitrepo.Commit{Hash: "working-tree", Parents: []string{head}}
}

// update 返回与 historyGraphWithWorking(commits, head, working) 相同的行。
func (cache *historyGraphCache) update(c *ui.Context, commits []gitrepo.Commit, head string, working bool) []historyGraphRow {
	source, font, zoom := firstCommit(commits), c.Theme().Font, c.Zoom()
	if cache.builder != nil && cache.source == source && cache.count == len(commits) && cache.head == head &&
		cache.working == working && cache.font == font && cache.zoom == zoom {
		return cache.rows
	}
	offset := 0
	if working {
		offset = 1
	}
	if cache.builder != nil && cache.head == head && cache.working == working && cache.extends(commits, offset) {
		known := len(cache.builder.hashes) - offset
		previous := len(cache.rows)
		rows, first := cache.builder.append(commits[known:])
		cache.rows = rows
		if first < previous {
			cache.width = historyGraphWidth(rows)
		} else {
			cache.width = max(cache.width, historyGraphWidth(rows[first:]))
		}
		for index := known; index < len(commits); index++ {
			if _, exists := cache.index[commits[index].Hash]; !exists {
				cache.index[commits[index].Hash] = index
			}
		}
	} else {
		*cache = historyGraphCache{builder: newHistoryGraphBuilder(), head: head, working: working, index: make(map[string]int, len(commits))}
		all := commits
		if working {
			all = make([]gitrepo.Commit, 0, len(commits)+1)
			all = append(append(all, historyWorkingCommit(head)), commits...)
		}
		cache.rows, _ = cache.builder.append(all)
		cache.width = historyGraphWidth(cache.rows)
		for index, commit := range commits {
			if _, exists := cache.index[commit.Hash]; !exists {
				cache.index[commit.Hash] = index
			}
		}
	}
	if len(cache.rows) > 0 {
		cache.rows[0].Working = working
	}
	cache.measureRefs(c, commits, font, zoom)
	cache.source, cache.count, cache.font, cache.zoom = source, len(commits), font, zoom
	return cache.rows
}

// extends 判断已计算的提交是否仍是 commits 的前缀。只在未命中时调用，最坏 O(N)。
func (cache *historyGraphCache) extends(commits []gitrepo.Commit, offset int) bool {
	known := len(cache.builder.hashes) - offset
	if known < 0 || known > len(commits) {
		return false
	}
	for index, commit := range commits[:known] {
		if commit.Hash != cache.builder.hashes[index+offset] || !slices.Equal(commit.Parents, cache.builder.parents[index+offset]) {
			return false
		}
	}
	return true
}

// measureRefs 只测量新增提交的引用；字体、缩放或已测量部分的引用文本变化时全部重测。
func (cache *historyGraphCache) measureRefs(c *ui.Context, commits []gitrepo.Commit, font string, zoom float32) {
	known := len(cache.refs)
	if cache.font != font || cache.zoom != zoom || known > len(commits) {
		known = 0
	}
	for index := range known {
		if cache.refs[index] != commits[index].Refs {
			known = 0
			break
		}
	}
	if known == 0 {
		cache.refs, cache.refsWidth = cache.refs[:0], 0
	}
	for _, commit := range commits[known:] {
		cache.refs = append(cache.refs, commit.Refs)
		if commit.Refs != "" {
			width, _ := c.MeasureText(0, historyRefs(commit.Refs, c.Theme())...)
			cache.refsWidth = max(cache.refsWidth, width)
		}
	}
}

// commit 按哈希查找 commits 中第一个匹配的提交；缓存不对应 commits 时退回线性查找。
func (cache *historyGraphCache) commit(commits []gitrepo.Commit, hash string) *gitrepo.Commit {
	if cache != nil && cache.builder != nil && cache.source == firstCommit(commits) && cache.count == len(commits) {
		if index, ok := cache.index[hash]; ok && index < len(commits) && commits[index].Hash == hash {
			return &commits[index]
		}
		return nil
	}
	for index := range commits {
		if commits[index].Hash == hash {
			return &commits[index]
		}
	}
	return nil
}

func historyGraphWidth(rows []historyGraphRow) float32 {
	width := float32(80)
	for _, row := range rows {
		width = max(width, 24+float32(row.Columns)*16)
	}
	return width
}

func firstCommit(commits []gitrepo.Commit) *gitrepo.Commit {
	if len(commits) == 0 {
		return nil
	}
	return &commits[0]
}
