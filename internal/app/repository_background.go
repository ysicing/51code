// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
)

type repositoryFileWatcher interface {
	Next(context.Context) ([]mygo.FileEvent, error)
	Close() error
}

const (
	// 监听触发的刷新先等 500ms 合并事件；连续刷新都没有变化时（如 dev server
	// 不断改写被忽略的构建目录）逐次加倍，最长 8s，有变化、窗口聚焦或用户操作后复位。
	repositoryRefreshDelay    = 500 * time.Millisecond
	repositoryRefreshMaxDelay = 8 * time.Second
	// 后台工作被弹窗、忙碌或冲突编辑挡住时的复查间隔。
	repositoryBackgroundRetry = time.Second
)

// 监听中断后的重建间隔；用完仍失败才提示用户。
var defaultRepositoryWatchRetryDelays = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}

type nativeRepositoryBackground struct {
	path         string
	ctx          context.Context
	cancel       context.CancelFunc
	refreshTimer *time.Timer
	fetchTimer   *time.Timer
	lastFetch    time.Time
	fetch        func(context.Context, string) error
	fetchEnabled bool
	watchError   string
	// refreshDelay 为 0 时使用 repositoryRefreshDelay。
	refreshDelay time.Duration
	// refreshNeed 是待处理变化的最晚观察时刻（收到监听批次或订阅建立的时间）；
	// readStarted 是最近一次已应用到界面的完整读取的开始时刻。读取开始晚于
	// refreshNeed 时，这些变化已被读到，排队的刷新可以跳过。
	refreshNeed time.Time
	readStarted time.Time
}

func (app *nativeApp) stopRepositoryBackground() {
	if state := app.background; state != nil {
		app.background = nil
		state.cancel()
		if state.refreshTimer != nil {
			state.refreshTimer.Stop()
		}
		if state.fetchTimer != nil {
			state.fetchTimer.Stop()
		}
		if app.problem == state.watchError {
			app.problem = ""
		}
	}
	if app.problem == app.autoFetchError {
		app.problem = ""
	}
	app.autoFetchError = ""
	if app.autoFetch != nil {
		app.autoFetch.cancel()
		app.autoFetch = nil
	}
}

func (app *nativeApp) restartRepositoryBackground() {
	previous := app.background
	app.stopRepositoryBackground()
	if app.closed || app.repository == nil || app.screen == "library" ||
		(!app.prefs.AutoRefreshFiles || app.watchFiles == nil) && !app.prefs.AutoFetchRemotes {
		return
	}
	ctx, cancel := context.WithCancel(app.ctx)
	state := &nativeRepositoryBackground{
		path: app.repository.snapshot.Path, ctx: ctx, cancel: cancel,
		lastFetch: time.Now(), fetch: app.service.FetchInBackground, fetchEnabled: app.prefs.AutoFetchRemotes,
	}
	if previous != nil && previous.path == state.path && previous.fetchEnabled && state.fetchEnabled {
		state.lastFetch = previous.lastFetch
	}
	app.background = state
	if app.prefs.AutoRefreshFiles && app.watchFiles != nil {
		app.startRepositoryWatches(state)
	}
	if app.prefs.AutoFetchRemotes {
		app.scheduleAutomaticFetch(state, time.Until(state.lastFetch.Add(app.automaticFetchInterval())))
	}
}

func (app *nativeApp) backgroundCurrent(state *nativeRepositoryBackground) bool {
	return !app.closed && app.background == state && state.ctx.Err() == nil &&
		app.repository != nil && app.repository.snapshot.Path == state.path && app.screen != "library"
}

func (app *nativeApp) backgroundAvailable() bool {
	return !app.busy && app.readTask == nil && app.focusRefresh == nil && app.autoFetch == nil &&
		app.dialog == nil && !app.conflictDirty()
}

// postQuiet 在主线程执行纯调度逻辑。Window.Update 每次都会整窗重绘，弹窗打开时
// 定时器每秒复查一次也会触发重绘；这里改用不重绘的 RunOnMain，仅在 fn 返回 true
// （确实改变了界面状态）时请求重绘。测试只注入 post，此时退回 post。
func (app *nativeApp) postQuiet(fn func() bool) {
	if app.runOnMain == nil {
		app.post(func() { fn() })
		return
	}
	win := app.win
	app.runOnMain(func() {
		if fn() && win != nil {
			win.Invalidate()
		}
	})
}

// noteRepositoryRead 记录一次已应用到界面的完整读取，started 为读取开始时间。
func (app *nativeApp) noteRepositoryRead(path string, started time.Time) {
	if state := app.background; state != nil && state.path == path && started.After(state.readStarted) {
		state.readStarted = started
	}
}

// settleRepositoryRefresh 按刷新结果调整监听刷新的退避延迟。
func (app *nativeApp) settleRepositoryRefresh(watch, changed bool) {
	state := app.background
	switch {
	case state == nil:
	case changed:
		state.refreshDelay = 0
	case watch:
		state.refreshDelay = min(max(state.refreshDelay, repositoryRefreshDelay)*2, repositoryRefreshMaxDelay)
	}
}

func repositoryWatchOptions(worktree bool) mygo.FileWatchOptions {
	options := mygo.FileWatchOptions{Recursive: true, Debounce: 250 * time.Millisecond, MaxDelay: time.Second}
	if worktree {
		options.HintsOnly = true
		options.Ignore = func(path string, directory bool) bool {
			switch filepath.Base(path) {
			case ".git", ".mygo", "node_modules", ".trail":
				return directory
			}
			return false
		}
		return options
	}
	options.Ignore = func(path string, directory bool) bool {
		first, _, _ := strings.Cut(path, "/")
		switch first {
		case "HEAD", "index", "packed-refs", "config", "config.worktree", "shallow", "refs",
			"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "BISECT_LOG",
			"rebase-merge", "rebase-apply", "sequencer":
			return strings.HasSuffix(path, ".lock")
		default:
			return true
		}
	}
	return options
}

func (app *nativeApp) startRepositoryWatches(state *nativeRepositoryBackground) {
	watch := app.watchFiles
	directories := append([]string{state.path}, app.watchDirectories...)
	delays := app.watchRetryDelays
	if delays == nil {
		delays = defaultRepositoryWatchRetryDelays
	}
	go func() {
		failures := 0
		for {
			started := time.Now()
			err := app.runRepositoryWatches(state, watch, directories)
			if err == nil || state.ctx.Err() != nil {
				return
			}
			// 稳定运行一段时间后再出错视为新的故障，重新计算重试次数。
			if time.Since(started) > time.Minute {
				failures = 0
			}
			// 超出监听上限重试也不会成功，直接提示；手动刷新与聚焦刷新仍可用。
			if errors.Is(err, mygo.ErrWatchLimit) || failures >= len(delays) {
				app.reportWatchFailure(state, err)
				return
			}
			timer := time.NewTimer(delays[failures])
			failures++
			select {
			case <-state.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

// runRepositoryWatches 建立一轮订阅并转发事件，直到后台会话结束（返回 nil）或任一订阅出错。
// 返回前关闭本轮全部订阅，保证重试时不会与旧订阅重叠。
func (app *nativeApp) runRepositoryWatches(state *nativeRepositoryBackground,
	watch func(context.Context, string, mygo.FileWatchOptions) (repositoryFileWatcher, error), directories []string) error {
	ctx, cancel := context.WithCancel(state.ctx)
	var running sync.WaitGroup
	// defer 逆序执行：先取消，再等转发协程关闭各自的订阅。
	defer running.Wait()
	defer cancel()
	seen := make(map[string]bool)
	failures := make(chan error, len(directories))
	for _, directory := range directories {
		directory = filepath.Clean(directory)
		if seen[directory] {
			continue
		}
		seen[directory] = true
		watcher, err := watch(ctx, directory, repositoryWatchOptions(directory == state.path))
		if err != nil {
			return err
		}
		running.Add(1)
		go func() {
			defer running.Done()
			defer watcher.Close()
			for {
				_, err := watcher.Next(ctx)
				if err != nil {
					failures <- err
					return
				}
				// 批次中的变化都在此刻之前被观察到；之后开始的完整读取一定能读到它们。
				received := time.Now()
				app.postQuiet(func() bool {
					app.queueRepositoryRefresh(state, received)
					return false
				})
			}
		}()
	}
	// 订阅建立前（含打开仓库读取之后）发生的变化可能没有事件，补查一次；
	// 若订阅建立后已有新的完整读取开始，补查会被跳过。
	subscribed := time.Now()
	app.postQuiet(func() bool {
		app.queueRepositoryRefresh(state, subscribed)
		return false
	})
	select {
	case <-ctx.Done():
		return nil
	case err := <-failures:
		return err
	}
}

func (app *nativeApp) reportWatchFailure(state *nativeRepositoryBackground, err error) {
	if state.ctx.Err() != nil {
		return
	}
	app.post(func() {
		if app.backgroundCurrent(state) {
			message := locales.AutomaticRefreshFailed(err.Error())
			if app.problem == "" || app.problem == state.watchError {
				app.problem = message
			}
			state.watchError = message
		}
	})
}

// queueRepositoryRefresh 为 observed 时刻之前观察到的变化排队刷新，延迟内的多次调用合并为一次。
func (app *nativeApp) queueRepositoryRefresh(state *nativeRepositoryBackground, observed time.Time) {
	if !app.backgroundCurrent(state) {
		return
	}
	if observed.After(state.refreshNeed) {
		app.pathSearch.invalidateQuickPaths()
		state.refreshNeed = observed
	}
	if state.refreshTimer != nil {
		return
	}
	delay := state.refreshDelay
	if delay <= 0 {
		delay = repositoryRefreshDelay
	}
	app.scheduleRepositoryRefresh(state, delay)
}

func (app *nativeApp) scheduleRepositoryRefresh(state *nativeRepositoryBackground, delay time.Duration) {
	if state.refreshTimer != nil {
		state.refreshTimer.Stop()
	}
	state.refreshTimer = time.AfterFunc(delay, func() {
		if state.ctx.Err() != nil {
			return
		}
		app.postQuiet(func() bool {
			if !app.backgroundCurrent(state) {
				return false
			}
			state.refreshTimer = nil
			// 写操作或聚焦刷新的完整读取已在这些变化之后开始，无需再读一次。
			if state.readStarted.After(state.refreshNeed) {
				return false
			}
			if !app.backgroundAvailable() {
				app.scheduleRepositoryRefresh(state, repositoryBackgroundRetry)
				return false
			}
			app.refreshRepositoryView(true)
			// 刷新开始时可能调整侧栏页面，按需重绘一次。
			return true
		})
	})
}

func (app *nativeApp) automaticFetchInterval() time.Duration {
	return time.Duration(app.prefs.AutoFetchMinutes) * time.Minute
}

func (app *nativeApp) scheduleAutomaticFetch(state *nativeRepositoryBackground, delay time.Duration) {
	if state.fetchTimer != nil {
		state.fetchTimer.Stop()
	}
	state.fetchTimer = time.AfterFunc(max(delay, time.Second), func() {
		if state.ctx.Err() != nil {
			return
		}
		// 启动或推迟 fetch 不改变界面内容，不重绘；结束时经 post 更新错误提示与刷新。
		app.postQuiet(func() bool {
			if app.backgroundCurrent(state) {
				state.fetchTimer = nil
				app.tryAutomaticFetch(state, time.Now())
			}
			return false
		})
	})
}

func (app *nativeApp) tryAutomaticFetch(state *nativeRepositoryBackground, now time.Time) {
	if !app.backgroundCurrent(state) || !app.prefs.AutoFetchRemotes {
		return
	}
	interval := app.automaticFetchInterval()
	if delay := interval - now.Sub(state.lastFetch); delay > 0 {
		app.scheduleAutomaticFetch(state, delay)
		return
	}
	if !app.backgroundAvailable() {
		app.scheduleAutomaticFetch(state, repositoryBackgroundRetry)
		return
	}
	app.scheduleAutomaticFetch(state, interval)
	state.lastFetch = now
	if !app.writable || len(app.repository.remotes) == 0 || app.repository.snapshot.Operation.Kind != "" {
		return
	}
	ctx, cancel := context.WithTimeout(state.ctx, 2*time.Minute)
	request := &nativeRead{cancel: cancel}
	previous, done := app.readDone, make(chan struct{})
	app.autoFetch, app.readDone, app.autoFetchDone = request, done, done
	post, fetch := app.post, state.fetch
	go func() {
		if previous != nil {
			<-previous
		}
		err := ctx.Err()
		if err == nil {
			err = fetch(ctx, state.path)
		}
		cancel()
		close(done)
		finished := time.Now()
		post(func() {
			if app.autoFetch != request {
				return
			}
			app.autoFetch = nil
			if !app.backgroundCurrent(state) {
				return
			}
			if err != nil && errors.Is(err, context.Canceled) {
				return
			}
			// 聚焦刷新会让位于进行中的 fetch，因此失败时也要补一次刷新，
			// 避免 fetch 期间外部修改的工作区一直不显示。
			app.queueRepositoryRefresh(state, finished)
			if err != nil {
				message := locales.AutomaticFetchFailed(err.Error())
				if app.problem == "" || app.problem == app.autoFetchError {
					app.problem = message
				}
				app.autoFetchError = message
				return
			}
			if app.problem == app.autoFetchError {
				app.problem = ""
			}
			app.autoFetchError = ""
		})
	}()
}
