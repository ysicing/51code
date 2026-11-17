// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"time"
)

// nativeCommitGraphs 为首次打开的仓库在后台补写 commit-graph。
// attempted 只在界面线程访问；slot 保证同一时间只写一个仓库，
// 资源等待器因此也不会被并发调用。
type nativeCommitGraphs struct {
	attempted        map[string]bool
	slot             chan struct{}
	waitForResources func(context.Context) error
	ensure           func(context.Context, string) (bool, error)
}

func (app *nativeApp) startCommitGraphs() {
	if app.desktop != nil && app.desktop.commitGraphs != nil {
		app.commitGraphs = app.desktop.commitGraphs
		return
	}
	app.commitGraphs = &nativeCommitGraphs{
		attempted: map[string]bool{}, slot: make(chan struct{}, 1),
		waitForResources: languageResourceWaiter(), ensure: app.service.EnsureCommitGraph,
	}
	if app.desktop != nil {
		app.desktop.commitGraphs = app.commitGraphs
	}
}

// maintainCommitGraph 每个仓库每次运行只尝试一次；失败（只读、锁冲突、旧版 Git 等）不提示，
// 不占用前台 busy，下次启动再试。
func (app *nativeApp) maintainCommitGraph(path string) {
	state := app.commitGraphs
	if state == nil || app.closed || path == "" || state.attempted[path] {
		return
	}
	state.attempted[path] = true
	ctx := app.ctx
	go func() {
		select {
		case state.slot <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-state.slot }()
		// 系统繁忙时推迟；等待超时则放弃本次，避免与用户的构建、测试争抢资源。
		if state.waitForResources != nil {
			if err := state.waitForResources(ctx); err != nil {
				return
			}
		}
		// 超时只约束写入前的检查；写入一旦开始会完整结束，避免留下锁文件。
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_, _ = state.ensure(checkCtx, path)
	}()
}
