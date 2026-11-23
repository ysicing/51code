// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"

	"github.com/egoist/mygo"
)

func runBackgroundUpdates(t *testing.T, updates <-chan func(), complete func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for !complete() {
		select {
		case update := <-updates:
			update()
		case <-deadline.C:
			t.Fatal("background operation did not finish")
		}
	}
}

type testRepositoryWatch struct {
	events chan []mygo.FileEvent
	closed chan struct{}
	once   sync.Once
}

func (watch *testRepositoryWatch) Next(ctx context.Context) ([]mygo.FileEvent, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case events := <-watch.events:
		return events, nil
	}
}

func (watch *testRepositoryWatch) Close() error {
	watch.once.Do(func() { close(watch.closed) })
	return nil
}

func TestNativeRepositoryWatchRefreshAndCleanup(t *testing.T) {
	app, path, _ := nativeFixture(t)
	updates := make(chan func(), 32)
	app.post = func(update func()) { updates <- update }
	created := make(chan *testRepositoryWatch, 8)
	app.watchFiles = func(context.Context, string, mygo.FileWatchOptions) (repositoryFileWatcher, error) {
		watch := &testRepositoryWatch{events: make(chan []mygo.FileEvent, 8), closed: make(chan struct{})}
		created <- watch
		return watch, nil
	}
	app.restartRepositoryBackground()
	t.Cleanup(app.stopRepositoryBackground)
	state := app.background
	var watch *testRepositoryWatch
	select {
	case watch = <-created:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not start")
	}
	app.busy = true
	writeNativeFile(t, path, "hello.txt", "外部编辑\n")
	for range 5 {
		watch.events <- []mygo.FileEvent{{Op: mygo.FileHint, Path: "."}}
	}
	runBackgroundUpdates(t, updates, func() bool { return state.refreshTimer != nil })
	timer := state.refreshTimer
	app.queueRepositoryRefresh(state, time.Now())
	if timer != state.refreshTimer || app.focusRefresh != nil {
		t.Fatal("watch events were not coalesced or interrupted a foreground task")
	}
	app.busy = false
	runBackgroundUpdates(t, updates, func() bool { return len(app.repository.snapshot.Files) == 1 && app.focusRefresh == nil })
	if app.repository.snapshot.Files[0].Path != "hello.txt" {
		t.Fatal("file events did not refresh the current repository")
	}
	app.showLibrary()
	select {
	case <-watch.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("returning to the library did not close the watch")
	}
	app.queueRepositoryRefresh(state, time.Now())
	if app.background != nil || app.autoFetch != nil || app.focusRefresh != nil {
		t.Fatal("obsolete watch restarted work after leaving the repository")
	}
}

func TestNativeRepositoryWatchFilters(t *testing.T) {
	worktree, metadata := repositoryWatchOptions(true), repositoryWatchOptions(false)
	if !worktree.HintsOnly || !worktree.Ignore(".git", true) || !worktree.Ignore("packages/ui/node_modules", true) ||
		worktree.Ignore("vendor", true) || worktree.Ignore("src", true) {
		t.Fatal("worktree watch did not use the expected lightweight directory filtering")
	}
	for _, path := range []string{"index", "HEAD", "refs/heads/main", "packed-refs", "rebase-merge/head-name", "config.worktree"} {
		if metadata.Ignore(path, false) {
			t.Fatalf("required metadata was ignored: %s", path)
		}
	}
	for _, path := range []string{"worktrees", "worktrees/codex"} {
		if metadata.Ignore(path, true) {
			t.Fatalf("worktree registration directory was ignored: %s", path)
		}
	}
	if !metadata.Recursive || !metadata.Ignore("worktrees/codex/logs", true) {
		t.Fatal("worktree metadata watch did not recurse while pruning per-worktree logs")
	}
	for _, path := range []string{"worktrees/codex/gitdir", "worktrees/codex/HEAD", "worktrees/codex/locked"} {
		if metadata.Ignore(path, false) {
			t.Fatalf("worktree registration metadata was ignored: %s", path)
		}
	}
	for _, path := range []string{"objects/ab/cd", "logs/HEAD", "FETCH_HEAD", "index.lock", "refs/heads/main.lock",
		"worktrees/codex/index", "worktrees/codex/logs/HEAD", "worktrees/codex/HEAD.lock"} {
		if !metadata.Ignore(path, false) {
			t.Fatalf("noisy metadata was watched: %s", path)
		}
	}
}

func TestNativeRepositoryWatchFailureRetainsManualRefresh(t *testing.T) {
	app, _, _ := nativeFixture(t)
	updates := make(chan func(), 32)
	app.post = func(update func()) { updates <- update }
	app.watchFiles = func(context.Context, string, mygo.FileWatchOptions) (repositoryFileWatcher, error) {
		return nil, mygo.ErrWatchLimit
	}
	app.restartRepositoryBackground()
	t.Cleanup(app.stopRepositoryBackground)
	runBackgroundUpdates(t, updates, func() bool { return app.problem != "" })
	app.refreshOnFocus()
	runBackgroundUpdates(t, updates, func() bool { return app.focusRefresh == nil })
	if !app.writable || app.repository == nil {
		t.Fatal("watch failure disabled repository refresh")
	}
	app.prefs.AutoRefreshFiles = false
	app.restartRepositoryBackground()
	if app.background != nil || app.problem != "" {
		t.Fatal("disabling a failed watch retained its state or error")
	}
}

func automaticFetchFixture(t *testing.T) (*nativeApp, *nativeRepositoryBackground, <-chan func()) {
	t.Helper()
	app, _, _ := nativeFixture(t)
	updates := make(chan func(), 32)
	app.post = func(update func()) { updates <- update }
	app.prefs.AutoFetchRemotes = true
	app.repository.remotes = []gitrepo.RemoteConfig{{Name: "origin"}}
	app.restartRepositoryBackground()
	t.Cleanup(app.stopRepositoryBackground)
	return app, app.background, updates
}

func TestNativeAutomaticFetchDefersAndThrottles(t *testing.T) {
	app, state, updates := automaticFetchFixture(t)
	calls := 0
	state.fetch = func(context.Context, string) error { calls++; return nil }
	now := time.Now()
	app.tryAutomaticFetch(state, now)
	if app.autoFetch != nil {
		t.Fatal("automatic fetch ran before its first interval")
	}
	state.lastFetch = now.Add(-app.automaticFetchInterval())
	app.dialog = &nativeDialog{}
	app.tryAutomaticFetch(state, now)
	if app.autoFetch != nil {
		t.Fatal("automatic fetch interrupted a dialog")
	}
	app.dialog = nil
	app.tryAutomaticFetch(state, now)
	runBackgroundUpdates(t, updates, func() bool { return app.autoFetch == nil })
	if calls != 1 || app.busy || app.problem != "" {
		t.Fatal("automatic fetch did not finish without entering global busy state")
	}
	app.tryAutomaticFetch(state, now.Add(time.Minute))
	if calls != 1 || app.autoFetch != nil {
		t.Fatal("automatic fetch ignored the configured interval")
	}
}

func TestNativeAutomaticFetchFirstEnableWaitsFullInterval(t *testing.T) {
	app, previous, _ := automaticFetchFixture(t)
	previous.fetchEnabled = false
	previous.lastFetch = time.Now().Add(-time.Hour)
	app.restartRepositoryBackground()
	state := app.background
	if state == previous || time.Since(state.lastFetch) > time.Second {
		t.Fatal("enabling fetch inherited the age of a watch-only background session")
	}
	app.tryAutomaticFetch(state, time.Now())
	if app.autoFetch != nil {
		t.Fatal("enabling automatic fetch started networking before its first interval")
	}
}

func TestNativeAutomaticFetchYieldsToForeground(t *testing.T) {
	app, state, updates := automaticFetchFixture(t)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	state.fetch = func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	}
	state.lastFetch = time.Now().Add(-app.automaticFetchInterval())
	app.tryAutomaticFetch(state, time.Now())
	<-started
	foreground := make(chan struct{})
	app.runTask("前台操作", true, func(context.Context) (func(), error) {
		close(foreground)
		return nil, nil
	})
	<-canceled
	select {
	case <-foreground:
		t.Fatal("foreground operation did not wait for canceled fetch cleanup")
	default:
	}
	close(release)
	runBackgroundUpdates(t, updates, func() bool { return !app.busy })
	if app.problem != "" || app.autoFetch != nil {
		t.Fatal("canceled background fetch surfaced an error or retained its request")
	}
}

func TestNativeDeferredFocusRefreshSurvivesCanceledFetch(t *testing.T) {
	app, state, updates := automaticFetchFixture(t)
	app.prefs.AutoRefreshFiles = false
	started := make(chan struct{})
	state.fetch = func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	state.lastFetch = time.Now().Add(-app.automaticFetchInterval())
	app.tryAutomaticFetch(state, time.Now())
	<-started
	writeNativeFile(t, state.path, "hello.txt", "外部编辑\n")
	app.refreshOnFocus()
	app.selectFile("hello.txt", false)
	runBackgroundUpdates(t, updates, func() bool {
		return app.readTask == nil && app.focusRefresh == nil && len(app.repository.snapshot.Files) == 1
	})
}

func TestNativeFocusRefreshDoesNotCancelAutomaticFetch(t *testing.T) {
	for _, fetchErr := range []error{nil, errors.New("offline")} {
		t.Run(fmt.Sprint(fetchErr), func(t *testing.T) {
			app, state, updates := automaticFetchFixture(t)
			path := app.repository.snapshot.Path
			started, release := make(chan struct{}), make(chan struct{})
			state.fetch = func(ctx context.Context, _ string) error {
				close(started)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-release:
					return fetchErr
				}
			}
			state.lastFetch = time.Now().Add(-app.automaticFetchInterval())
			app.tryAutomaticFetch(state, time.Now())
			<-started
			writeNativeFile(t, path, "hello.txt", "fetch 期间的外部修改\n")
			app.refreshOnFocus()
			if app.autoFetch == nil || app.focusRefresh != nil {
				t.Fatal("聚焦刷新取消了进行中的自动 fetch")
			}
			close(release)
			runBackgroundUpdates(t, updates, func() bool {
				return app.autoFetch == nil && app.focusRefresh == nil && len(app.repository.snapshot.Files) == 1
			})
			if !app.writable || (fetchErr == nil) != (app.problem == "") {
				t.Fatalf("fetch 结束后的状态异常: writable=%v problem=%q", app.writable, app.problem)
			}
		})
	}
}

func TestNativeAutomaticFetchFailureDoesNotDisableRepository(t *testing.T) {
	app, state, updates := automaticFetchFixture(t)
	state.fetch = func(context.Context, string) error { return errors.New("offline") }
	state.lastFetch = time.Now().Add(-app.automaticFetchInterval())
	app.tryAutomaticFetch(state, time.Now())
	runBackgroundUpdates(t, updates, func() bool { return app.autoFetch == nil })
	if app.problem == "" || !app.writable || app.busy {
		t.Fatal("background network failure disabled the repository or was lost")
	}
	app.prefs.AutoFetchRemotes = false
	app.restartRepositoryBackground()
	if app.background != nil {
		t.Fatal("disabling background behavior did not release its timers")
	}
}

func TestNativeAutomaticFetchStopsOnNavigation(t *testing.T) {
	app, state, updates := automaticFetchFixture(t)
	started := make(chan struct{})
	state.fetch = func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	state.lastFetch = time.Now().Add(-app.automaticFetchInterval())
	app.tryAutomaticFetch(state, time.Now())
	<-started
	app.showLibrary()
	select {
	case <-app.autoFetchDone:
	case <-time.After(3 * time.Second):
		t.Fatal("navigation did not finish canceling automatic fetch")
	}
	runBackgroundUpdates(t, updates, func() bool { return len(updates) == 0 })
	if app.background != nil || app.autoFetch != nil || app.problem != "" || app.screen != "library" {
		t.Fatal("a canceled fetch changed the library or retained its background state")
	}
}

func watchedBackgroundFixture(t *testing.T) (*nativeApp, string, *nativeRepositoryBackground, chan func(), chan *testRepositoryWatch) {
	t.Helper()
	app, path, _ := nativeFixture(t)
	updates := make(chan func(), 32)
	app.post = func(update func()) { updates <- update }
	created := make(chan *testRepositoryWatch, 8)
	app.watchFiles = func(context.Context, string, mygo.FileWatchOptions) (repositoryFileWatcher, error) {
		watch := &testRepositoryWatch{events: make(chan []mygo.FileEvent, 8), closed: make(chan struct{})}
		created <- watch
		return watch, nil
	}
	app.restartRepositoryBackground()
	t.Cleanup(app.stopRepositoryBackground)
	return app, path, app.background, updates, created
}

func TestNativeRepositoryWatchBacksOffUnchangedRefreshes(t *testing.T) {
	app, path, state, updates, _ := watchedBackgroundFixture(t)
	// 订阅建立后的补查也是监听刷新；仓库未变化时开始退避。
	runBackgroundUpdates(t, updates, func() bool { return !state.readStarted.IsZero() && app.focusRefresh == nil })
	want := 2 * repositoryRefreshDelay
	for state.refreshDelay < repositoryRefreshMaxDelay {
		if state.refreshDelay != want {
			t.Fatalf("无变化刷新的退避延迟 = %v，期望 %v", state.refreshDelay, want)
		}
		app.refreshRepositoryView(true)
		runBackgroundUpdates(t, updates, func() bool { return app.focusRefresh == nil })
		want = min(want*2, repositoryRefreshMaxDelay)
	}
	app.refreshRepositoryView(true)
	runBackgroundUpdates(t, updates, func() bool { return app.focusRefresh == nil })
	if state.refreshDelay != repositoryRefreshMaxDelay {
		t.Fatalf("退避超过上限: %v", state.refreshDelay)
	}
	writeNativeFile(t, path, "hello.txt", "真实变化\n")
	app.refreshRepositoryView(true)
	runBackgroundUpdates(t, updates, func() bool { return app.focusRefresh == nil })
	if state.refreshDelay != 0 || len(app.repository.snapshot.Files) != 1 {
		t.Fatal("仓库变化后没有复位退避")
	}
	state.refreshDelay = repositoryRefreshMaxDelay
	app.refreshOnFocus()
	if state.refreshDelay != 0 {
		t.Fatal("窗口聚焦没有复位退避")
	}
	runBackgroundUpdates(t, updates, func() bool { return app.focusRefresh == nil })
	state.refreshDelay = repositoryRefreshMaxDelay
	app.runTask("用户操作", true, func(context.Context) (func(), error) { return nil, nil })
	runBackgroundUpdates(t, updates, func() bool { return !app.busy })
	if state.refreshDelay != 0 {
		t.Fatal("用户操作后没有复位退避")
	}
}

func TestNativeRepositoryWatchSkipsChangesCoveredByLaterRead(t *testing.T) {
	app, path, state, updates, _ := watchedBackgroundFixture(t)
	runBackgroundUpdates(t, updates, func() bool { return !state.readStarted.IsZero() && app.focusRefresh == nil })
	// 用上一轮读取时间模拟已经排队的事件，避免紧邻的 time.Now 在 Windows 上取到同一时钟刻度。
	observed := state.readStarted
	app.refresh()
	runBackgroundUpdates(t, updates, func() bool { return !app.busy })
	covered := state.readStarted
	if !covered.After(observed) {
		t.Fatalf("手动刷新的完整读取没有登记：刷新错误=%q", app.problem)
	}
	state.refreshDelay = 0
	app.queueRepositoryRefresh(state, observed)
	runBackgroundUpdates(t, updates, func() bool { return state.refreshTimer == nil })
	if app.focusRefresh != nil || !state.readStarted.Equal(covered) {
		t.Fatal("已被后续读取覆盖的变化又触发了完整读取")
	}
	// 读取开始之后才观察到的变化必须刷新。
	writeNativeFile(t, path, "hello.txt", "读取之后的变化\n")
	app.queueRepositoryRefresh(state, time.Now())
	runBackgroundUpdates(t, updates, func() bool { return state.readStarted.After(covered) && app.focusRefresh == nil })
	if len(app.repository.snapshot.Files) != 1 {
		t.Fatal("读取开始后的变化被跳过")
	}
}

func TestNativeRepositoryWatchRetriesAfterFailure(t *testing.T) {
	app, _, _ := nativeFixture(t)
	updates := make(chan func(), 32)
	app.post = func(update func()) { updates <- update }
	app.watchRetryDelays = []time.Duration{10 * time.Millisecond}
	created := make(chan *testRepositoryWatch, 8)
	failures := make(chan error, 8)
	app.watchFiles = func(context.Context, string, mygo.FileWatchOptions) (repositoryFileWatcher, error) {
		watch := &testRepositoryWatch{events: make(chan []mygo.FileEvent, 8), closed: make(chan struct{})}
		created <- watch
		return &failingRepositoryWatch{testRepositoryWatch: watch, failures: failures}, nil
	}
	app.restartRepositoryBackground()
	t.Cleanup(app.stopRepositoryBackground)
	first := <-created
	failures <- errors.New("stream ended")
	select {
	case <-first.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("失败的订阅没有关闭")
	}
	var second *testRepositoryWatch
	select {
	case second = <-created:
	case <-time.After(5 * time.Second):
		t.Fatal("监听出错后没有重建订阅")
	}
	second.events <- []mygo.FileEvent{{Op: mygo.FileHint, Path: "."}}
	runBackgroundUpdates(t, updates, func() bool { return app.background.refreshTimer != nil })
	if app.problem != "" {
		t.Fatal("可恢复的监听错误直接提示了用户")
	}
	failures <- errors.New("stream ended again")
	runBackgroundUpdates(t, updates, func() bool { return app.problem != "" })
	if len(created) != 0 {
		t.Fatal("重试次数用完后仍在重建订阅")
	}
}

type failingRepositoryWatch struct {
	*testRepositoryWatch
	failures chan error
}

func (watch *failingRepositoryWatch) Next(ctx context.Context) ([]mygo.FileEvent, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-watch.failures:
		return nil, err
	case events := <-watch.events:
		return events, nil
	}
}

func TestNativeBackgroundPollingDoesNotRedraw(t *testing.T) {
	app, _, _ := nativeFixture(t)
	updates, quiet := make(chan func(), 32), make(chan func(), 32)
	app.post = func(update func()) { updates <- update }
	app.runOnMain = func(fn func()) { quiet <- fn }
	app.watchFiles = func(context.Context, string, mygo.FileWatchOptions) (repositoryFileWatcher, error) {
		return &testRepositoryWatch{events: make(chan []mygo.FileEvent), closed: make(chan struct{})}, nil
	}
	app.restartRepositoryBackground()
	t.Cleanup(app.stopRepositoryBackground)
	state := app.background
	nextQuiet := func() {
		t.Helper()
		select {
		case fn := <-quiet:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal("后台调度没有执行")
		}
	}
	// 订阅补查：排队与启动刷新都走不重绘的主线程调度，只有刷新结果经 post 更新界面。
	for app.focusRefresh == nil {
		nextQuiet()
	}
	if len(updates) != 0 {
		t.Fatal("排队刷新触发了整窗重绘")
	}
	runBackgroundUpdates(t, updates, func() bool { return app.focusRefresh == nil })
	app.dialog = &nativeDialog{}
	state.refreshDelay = 0
	app.queueRepositoryRefresh(state, time.Now())
	for range 3 {
		nextQuiet()
		if len(updates) != 0 || app.focusRefresh != nil || state.refreshTimer == nil {
			t.Fatal("弹窗期间的刷新复查触发了重绘、打断了弹窗或丢失了排队")
		}
	}
	app.dialog = nil
	for app.focusRefresh == nil {
		nextQuiet()
	}
	runBackgroundUpdates(t, updates, func() bool { return app.focusRefresh == nil })
}
