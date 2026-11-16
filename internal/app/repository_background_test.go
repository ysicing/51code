// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
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
	app.queueRepositoryRefresh(state)
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
	app.queueRepositoryRefresh(state)
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
	for _, path := range []string{"objects/ab/cd", "logs/HEAD", "FETCH_HEAD", "index.lock", "refs/heads/main.lock", "worktrees/other/HEAD"} {
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

func TestNativeAutomaticFetchYieldsToFocusRefresh(t *testing.T) {
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
	app.refreshOnFocus()
	if app.autoFetch != nil || app.focusRefresh == nil {
		t.Fatal("focus refresh waited for networking instead of canceling the background fetch")
	}
	runBackgroundUpdates(t, updates, func() bool { return app.focusRefresh == nil })
	if app.problem != "" || !app.writable {
		t.Fatal("canceling fetch for focus refresh changed the repository's usable state")
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
