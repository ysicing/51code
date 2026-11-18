// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"github.com/ysicing/51code/internal/gitrepo"
)

// nativeRefSource 标识一次引用快照。刷新会整体替换 repository；测试和少数路径会原地
// 替换或追加 branches、tags、remotes，因此同时比较各切片首元素地址与长度。
// 引用节点只由这三个切片决定（当前分支、默认分支都来自 branches），任一变化即失效。
// 前提是生产代码不原地改写这些切片的元素（已审计）；测试修改引用时也要换新切片。
type nativeRefSource struct {
	repository  *repositoryView
	branches    *gitrepo.Branch
	branchCount int
	tags        *gitrepo.Tag
	tagCount    int
	remotes     *gitrepo.RemoteConfig
	remoteCount int
}

func (app *nativeApp) refSource() nativeRefSource {
	source := nativeRefSource{repository: app.repository}
	if repository := app.repository; repository != nil {
		source.branchCount, source.tagCount, source.remoteCount = len(repository.branches), len(repository.tags), len(repository.remotes)
		if len(repository.branches) > 0 {
			source.branches = &repository.branches[0]
		}
		if len(repository.tags) > 0 {
			source.tags = &repository.tags[0]
		}
		if len(repository.remotes) > 0 {
			source.remotes = &repository.remotes[0]
		}
	}
	return source
}

// nativeRefTree 缓存一个引用分组（本地分支、标签或远端）的节点树，挂在分组的 ui.Local 上。
// 节点在分组展开时才构建，折叠的分组（标签默认折叠）不再每遍构建。节点构建后只读。
type nativeRefTree struct {
	valid  bool
	source nativeRefSource
	nodes  []*nativeRefNode
	// branches 仅本地分支分组使用，按名称查分支详情。
	branches map[string]gitrepo.Branch
}

// sync 在引用快照变化时调用 build 重建节点。
func (tree *nativeRefTree) sync(source nativeRefSource, build func(*nativeRefTree)) {
	if tree.valid && tree.source == source {
		return
	}
	*tree = nativeRefTree{valid: true, source: source}
	build(tree)
}

// nativeComparisonReferences 缓存比较页的候选引用列表，失效条件同 nativeRefSource。
type nativeComparisonReferences struct {
	valid      bool
	source     nativeRefSource
	references []string
}
