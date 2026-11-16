// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
)

type repositoryFileWatcher interface {
	Next(context.Context) ([]mygo.FileEvent, error)
	Close() error
}

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
	watch, post := app.watchFiles, app.post
	directories := append([]string{state.path}, app.watchDirectories...)
	go func() {
		ctx, cancel := context.WithCancel(state.ctx)
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
				app.reportWatchFailure(state, err)
				return
			}
			go func() {
				defer watcher.Close()
				for {
					_, err := watcher.Next(ctx)
					if err != nil {
						failures <- err
						return
					}
					post(func() {
						if app.backgroundCurrent(state) {
							app.queueRepositoryRefresh(state)
						}
					})
				}
			}()
		}
		// 订阅前发生的变化可能没有事件，完成订阅后补查一次。
		post(func() {
			if app.backgroundCurrent(state) {
				app.queueRepositoryRefresh(state)
			}
		})
		select {
		case <-ctx.Done():
		case err := <-failures:
			app.reportWatchFailure(state, err)
		}
	}()
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

func (app *nativeApp) queueRepositoryRefresh(state *nativeRepositoryBackground) {
	if !app.backgroundCurrent(state) || state.refreshTimer != nil {
		return
	}
	app.scheduleRepositoryRefresh(state, 500*time.Millisecond)
}

func (app *nativeApp) scheduleRepositoryRefresh(state *nativeRepositoryBackground, delay time.Duration) {
	post := app.post
	if state.refreshTimer != nil {
		state.refreshTimer.Stop()
	}
	state.refreshTimer = time.AfterFunc(delay, func() {
		if state.ctx.Err() != nil {
			return
		}
		post(func() {
			if !app.backgroundCurrent(state) {
				return
			}
			state.refreshTimer = nil
			if !app.backgroundAvailable() {
				app.scheduleRepositoryRefresh(state, time.Second)
				return
			}
			app.refreshOnFocus()
		})
	})
}

func (app *nativeApp) automaticFetchInterval() time.Duration {
	return time.Duration(app.prefs.AutoFetchMinutes) * time.Minute
}

func (app *nativeApp) scheduleAutomaticFetch(state *nativeRepositoryBackground, delay time.Duration) {
	post := app.post
	if state.fetchTimer != nil {
		state.fetchTimer.Stop()
	}
	state.fetchTimer = time.AfterFunc(max(delay, time.Second), func() {
		if state.ctx.Err() != nil {
			return
		}
		post(func() {
			if app.backgroundCurrent(state) {
				state.fetchTimer = nil
				app.tryAutomaticFetch(state, time.Now())
			}
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
		app.scheduleAutomaticFetch(state, time.Second)
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
		post(func() {
			if app.autoFetch != request {
				return
			}
			app.autoFetch = nil
			if !app.backgroundCurrent(state) {
				return
			}
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					message := locales.AutomaticFetchFailed(err.Error())
					if app.problem == "" || app.problem == app.autoFetchError {
						app.problem = message
					}
					app.autoFetchError = message
				}
				return
			}
			if app.problem == app.autoFetchError {
				app.problem = ""
			}
			app.autoFetchError = ""
			app.queueRepositoryRefresh(state)
		})
	}()
}
