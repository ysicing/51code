// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"

	"github.com/egoist/mygo/ui"
)

// referenceCommitGraph 每次从头计算完整图谱，用于验证增量计算。
func referenceCommitGraph(commits []gitrepo.Commit) []historyGraphRow {
	positions := make(map[string]int, len(commits))
	for index, commit := range commits {
		positions[commit.Hash] = index
	}
	var lanes []historyLane
	nextColor := 0
	result := make([]historyGraphRow, len(commits))
	for index, commit := range commits {
		row := &result[index]
		for _, lane := range lanes {
			row.Incoming = append(row.Incoming, lane.Color)
		}
		column := slices.IndexFunc(lanes, func(lane historyLane) bool { return lane.Hash == commit.Hash })
		if column < 0 {
			column = len(lanes)
			lanes = append(lanes, historyLane{Hash: commit.Hash, Color: nextColor})
			nextColor++
		}
		row.Column, row.Color = column, lanes[column].Color
		after := slices.Clone(lanes)
		after = slices.Delete(after, column, column+1)
		for parentIndex, parent := range commit.Parents {
			if position, ok := positions[parent]; !ok || position <= index {
				continue
			}
			target := slices.IndexFunc(after, func(lane historyLane) bool { return lane.Hash == parent })
			if target >= 0 {
				if parentIndex == 0 && row.Color < after[target].Color {
					after[target].Color = row.Color
				}
				continue
			}
			lane := historyLane{Hash: parent, Color: row.Color}
			if parentIndex == 0 {
				after = slices.Insert(after, min(column, len(after)), lane)
			} else {
				lane.Color = nextColor
				nextColor++
				after = append(after, lane)
			}
		}
		for from, lane := range lanes {
			targets := []string{lane.Hash}
			if from == column {
				targets = commit.Parents
			}
			for _, hash := range targets {
				to := slices.IndexFunc(after, func(lane historyLane) bool { return lane.Hash == hash })
				if to >= 0 {
					color := lane.Color
					if from == column && hash != commit.Parents[0] {
						color = after[to].Color
					}
					row.Edges = append(row.Edges, historyEdge{From: from, To: to, Color: color})
				}
			}
		}
		row.Columns = max(len(lanes), len(after))
		lanes = after
	}
	return result
}

// randomHistory 生成拓扑有序的历史：父提交位于子提交之下，含合并、多根、
// 指向远处的父提交（跨页）和被筛选掉的父提交；duplicates 时混入重复哈希。
func randomHistory(random *rand.Rand, count int, duplicates bool) []gitrepo.Commit {
	commits := make([]gitrepo.Commit, count)
	for index := range commits {
		commits[index].Hash = fmt.Sprintf("c%d", index)
	}
	for index := range commits {
		parents := 1 + random.Intn(3)
		if random.Intn(10) < 6 {
			parents = 1
		}
		for range parents {
			switch roll := random.Intn(20); {
			case roll == 0:
				commits[index].Parents = append(commits[index].Parents, fmt.Sprintf("hidden%d", random.Intn(count)))
			case index+1 < count && roll < 4:
				// 指向很远的父提交，制造跨页父提交与多条并行轨道。
				commits[index].Parents = append(commits[index].Parents, commits[index+1+random.Intn(count-index-1)].Hash)
			case index+1 < count:
				commits[index].Parents = append(commits[index].Parents, commits[min(count-1, index+1+random.Intn(4))].Hash)
			}
		}
	}
	if duplicates {
		for range max(1, count/50) {
			commits[random.Intn(count)].Hash = commits[random.Intn(count)].Hash
		}
	}
	// 模拟筛选：删掉部分提交，其子提交的父提交变为“缺失”。
	filtered := commits[:0:0]
	for _, commit := range commits {
		if random.Intn(8) != 0 {
			filtered = append(filtered, commit)
		}
	}
	return filtered
}

func TestHistoryGraphBuilderMatchesFullGraphAcrossPages(t *testing.T) {
	random := rand.New(rand.NewSource(1))
	for round := range 40 {
		count := 50 + random.Intn(1200)
		commits := randomHistory(random, count, round%4 == 3)
		for _, page := range []int{1, 7, 100, 333, len(commits)} {
			builder := newHistoryGraphBuilder()
			for start := 0; start < len(commits); start += page {
				end := min(len(commits), start+page)
				rows, first := builder.append(commits[start:end])
				want := referenceCommitGraph(commits[:end])
				if !reflect.DeepEqual(rows, want) {
					index := 0
					for ; index < min(len(rows), len(want))-1; index++ {
						if !reflect.DeepEqual(rows[index], want[index]) {
							break
						}
					}
					t.Fatalf("round=%d page=%d end=%d 第 %d 行不一致:\n got=%+v\nwant=%+v", round, page, end, index, rows[index], want[index])
				}
				if first > start {
					t.Fatalf("first=%d 晚于新增起点 %d", first, start)
				}
			}
		}
	}
}

func TestHistoryGraphBuilderRewindsWhenHiddenParentArrives(t *testing.T) {
	commits := []gitrepo.Commit{
		{Hash: "tip", Parents: []string{"base"}},
		{Hash: "side", Parents: []string{"other"}},
		{Hash: "other"},
		{Hash: "base"},
	}
	builder := newHistoryGraphBuilder()
	before, _ := builder.append(commits[:2])
	if len(before[0].Edges) != 0 {
		t.Fatalf("第一页不应连接到未加载的父提交: %+v", before[0])
	}
	rows, first := builder.append(commits[2:])
	if first != 0 {
		t.Fatalf("父提交出现在新页后应从第 0 行重算，first=%d", first)
	}
	if want := referenceCommitGraph(commits); !reflect.DeepEqual(rows, want) {
		t.Fatalf("got=%+v\nwant=%+v", rows, want)
	}
	if len(rows[0].Edges) == 0 {
		t.Fatal("翻页后第一行应连到 base")
	}
}

func TestHistoryGraphKeepsColorsWhenLinesConverge(t *testing.T) {
	cases := []struct {
		name     string
		commits  []gitrepo.Commit
		mainRows []int
		sideRows []int
		joinRow  int
	}{
		{
			name: "side line joins a pending main parent",
			commits: []gitrepo.Commit{
				{Hash: "tip", Parents: []string{"main"}},
				{Hash: "side", Parents: []string{"main"}},
				{Hash: "main", Parents: []string{"base"}},
				{Hash: "other", Parents: []string{"base"}},
				{Hash: "base"},
			},
			mainRows: []int{0, 2, 4}, sideRows: []int{1, 3}, joinRow: 1,
		},
		{
			name: "main line reaches an ancestor found through the side line first",
			commits: []gitrepo.Commit{
				{Hash: "merge", Parents: []string{"main", "side"}},
				{Hash: "side", Parents: []string{"base"}},
				{Hash: "main", Parents: []string{"base"}},
				{Hash: "base"},
			},
			mainRows: []int{0, 2, 3}, sideRows: []int{1}, joinRow: 2,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rows := commitGraph(test.commits)
			mainColor := rows[test.mainRows[0]].Color
			for _, index := range test.mainRows {
				if rows[index].Color != mainColor {
					t.Fatalf("%s 改变了主线颜色: got=%d want=%d", test.commits[index].Hash, rows[index].Color, mainColor)
				}
			}
			for _, index := range test.sideRows {
				if rows[index].Color == mainColor {
					t.Fatalf("%s 的支线颜色与主线相同", test.commits[index].Hash)
				}
			}
			join := rows[test.joinRow]
			if len(join.Edges) != 2 || join.Edges[0].To != join.Edges[1].To ||
				join.Edges[0].Color != mainColor || join.Edges[1].From != 1 ||
				join.Edges[1].Color != rows[test.sideRows[0]].Color {
				t.Fatalf("两条线应各自保持颜色直到汇合: %+v", join)
			}
		})
	}
}

func TestHistoryGraphCacheReusesPagesAndTracksChanges(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	commits := randomHistory(random, 600, false)
	commits[3].Refs = "HEAD -> main, origin/main"
	cache := &historyGraphCache{}
	var history []gitrepo.Commit
	step := func(c *ui.Context) {
		for _, working := range []bool{false, true} {
			got := cache.update(c, history, "c0", working)
			want := historyGraphWithWorking(history, "c0", working)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("len=%d working=%t 缓存图谱与全量结果不一致", len(history), working)
			}
			if width := historyGraphWidth(want); cache.width != width {
				t.Fatalf("图谱列宽=%v，want %v", cache.width, width)
			}
		}
	}
	var refsWidth float32
	ui.NewTester(func(c *ui.Context) {
		// 模拟翻页：append 可能复用底层数组（首地址不变），也可能重新分配。
		history = nil
		for start := 0; start < len(commits); start += 150 {
			history = append(history, commits[start:min(len(commits), start+150)]...)
			step(c)
		}
		refsWidth = cache.refsWidth
		if commit := cache.commit(history, commits[200].Hash); commit == nil || commit.Hash != commits[200].Hash {
			t.Fatalf("按哈希查找提交失败: %+v", commit)
		}
		if commit := cache.commit(history, "missing"); commit != nil {
			t.Fatalf("不存在的哈希返回了 %+v", commit)
		}
		// 刷新后提交不变但引用变化：必须重新测量引用宽度。
		refreshed := slices.Clone(history)
		refreshed[3].Refs = ""
		history = refreshed
		step(c)
		if cache.refsWidth != 0 {
			t.Fatalf("引用移除后引用宽度应归零，got %v", cache.refsWidth)
		}
		// 换搜索条件：前缀不再一致，必须整表重算。
		history = slices.Clone(commits[100:300])
		step(c)
	}, 320, 240)
	if refsWidth <= 0 {
		t.Fatal("带引用的提交应测得引用宽度")
	}
}
