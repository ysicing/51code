// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
)

// nativeDesktop 只在界面线程访问；共享偏好与仓库库，窗口各自拥有任务和界面状态。
type nativeDesktop struct {
	service      *RepositoryService
	stateFile    string
	prefs        *nativePreferences
	applied      nativePreferences
	library      *nativeApp
	active       *nativeApp
	repositories map[string]*nativeApp
	opening      map[string]*nativeRepositoryOpening
	commitGraphs *nativeCommitGraphs
	showWindow   func(*nativeApp)
	quitting     bool
	checkingQuit bool
}

type nativeRepositoryOpening struct {
	source *nativeApp
	done   chan struct{}
}

func newNativeDesktop(service *RepositoryService, stateFile string) *nativeDesktop {
	library := newNativeApp(service, stateFile)
	desktop := &nativeDesktop{
		service: service, stateFile: library.stateFile, prefs: library.prefs, applied: *library.prefs,
		library: library, repositories: make(map[string]*nativeApp), opening: make(map[string]*nativeRepositoryOpening),
	}
	desktop.showWindow = func(app *nativeApp) {
		if app.win == nil {
			app.open()
		} else {
			app.win.Show()
		}
		mygo.App.Focus()
		app.win.Focus()
	}
	library.desktop = desktop
	return desktop
}

func (desktop *nativeDesktop) present(app *nativeApp) {
	desktop.active = app
	if app.repository != nil && !app.repository.snapshot.LinkedWorktree && app.prefs.LastRepository != app.repository.snapshot.Path {
		app.prefs.LastRepository = app.repository.snapshot.Path
		if err := app.savePreferences(); err != nil {
			app.problem = locales.SaveSettingsError(err.Error())
		}
	}
	desktop.showWindow(app)
}

func repositoryWindowKey(path string) string {
	return fmt.Sprintf("repository-%x", sha256.Sum256([]byte(path)))
}

func (desktop *nativeDesktop) newApp() *nativeApp {
	// 服务的历史与扫描取消器属于窗口，不能随 manager 一起共享。
	app := newNativeApp(&RepositoryService{manager: desktop.service.manager}, "")
	app.desktop, app.prefs, app.stateFile = desktop, desktop.prefs, desktop.stateFile
	return app
}

func (desktop *nativeDesktop) showLibrary() *nativeApp {
	for path, request := range desktop.opening {
		if request.source.closed {
			select {
			case <-request.done:
				delete(desktop.opening, path)
			default:
			}
		}
	}
	if desktop.library == nil || desktop.library.closed {
		desktop.library = desktop.newApp()
	}
	desktop.library.library = desktop.service.Library()
	desktop.present(desktop.library)
	return desktop.library
}

func (desktop *nativeDesktop) windows() []*nativeApp {
	var windows []*nativeApp
	if desktop.library != nil && !desktop.library.closed {
		windows = append(windows, desktop.library)
	}
	paths := make([]string, 0, len(desktop.repositories))
	for path := range desktop.repositories {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		if app := desktop.repositories[path]; !app.closed {
			windows = append(windows, app)
		}
	}
	return windows
}

func (desktop *nativeDesktop) target(win *mygo.Window) *nativeApp {
	if win == nil {
		win = mygo.FocusedWindow()
		if win == nil && desktop.active != nil && !desktop.active.closed {
			return desktop.active
		}
	}
	for _, app := range desktop.windows() {
		if app.win != nil && app.win == win {
			return app
		}
	}
	return nil
}

func (app *nativeApp) gitToolsBusy() bool {
	if app.desktop != nil {
		for _, window := range app.desktop.windows() {
			if window.busy && !window.readOnlyBusy {
				return true
			}
		}
	}
	return false
}

func (desktop *nativeDesktop) openRepository(source *nativeApp, path string) {
	if desktop.checkingQuit || desktop.quitting {
		return
	}
	if app := desktop.repositories[path]; app != nil && !app.closed {
		desktop.present(app)
		desktop.markRepositoryOpened(source, path)
		return
	}
	source.runTask(locales.OpeningRepository(), true, func(ctx context.Context) (func(), error) {
		repo, err := gitrepo.Open(ctx, path)
		if err != nil {
			return source.repositoryOpenFailure(path, err)
		}
		return func() { desktop.openCanonicalRepository(source, repo.Path) }, nil
	})
}

func (desktop *nativeDesktop) openCanonicalRepository(source *nativeApp, path string) {
	if app := desktop.repositories[path]; app != nil && !app.closed {
		desktop.present(app)
		desktop.markRepositoryOpened(source, path)
		return
	}
	if source.closed || source.busy || desktop.checkingQuit || desktop.quitting {
		return
	}
	// 加载也按规范路径去重；旧窗口关闭后先等其 Git 读取退出，避免共享 manager 中出现重叠打开。
	previous := desktop.opening[path]
	if previous != nil && !previous.source.closed {
		desktop.present(previous.source)
		return
	}
	request := &nativeRepositoryOpening{source: source, done: make(chan struct{})}
	desktop.opening[path] = request
	source.runTask(locales.OpeningRepository(), true, func(ctx context.Context) (func(), error) {
		defer close(request.done)
		if previous != nil {
			select {
			case <-previous.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		clearRequest := func() {
			if desktop.opening[path] == request {
				delete(desktop.opening, path)
			}
		}
		result, err := source.readRepository(ctx, path, true)
		if err != nil {
			apply, err := source.repositoryOpenFailure(path, err)
			return func() {
				clearRequest()
				if apply != nil {
					apply()
				}
			}, err
		}
		directories, watchErr := source.service.WatchDirectories(ctx, result.snapshot.Path)
		return func() {
			clearRequest()
			if desktop.checkingQuit || desktop.quitting {
				return
			}
			if app := desktop.repositories[path]; app != nil && !app.closed {
				desktop.refreshLibraries()
				desktop.present(app)
				return
			}
			app := desktop.newApp()
			app.adopt(result)
			app.screen, app.watchDirectories = "changes", directories
			desktop.repositories[path] = app
			desktop.refreshLibraries()
			desktop.present(app)
			if watchErr != nil && app.prefs.AutoRefreshFiles && app.background != nil {
				app.reportWatchFailure(app.background, watchErr)
			}
		}, nil
	})
}

func (desktop *nativeDesktop) markRepositoryOpened(source *nativeApp, path string) {
	source.runTask(locales.OpeningRepository(), true, func(ctx context.Context) (func(), error) {
		_, err := desktop.service.MarkRepositoryOpened(ctx, path)
		return desktop.refreshLibraries, err
	})
}

func (desktop *nativeDesktop) refreshLibraries() {
	for _, app := range desktop.windows() {
		app.library = desktop.service.Library()
		if app.win != nil {
			app.win.Invalidate()
		}
	}
}

func (desktop *nativeDesktop) preferencesChanged(source *nativeApp) {
	previous := desktop.applied
	desktop.applied = *desktop.prefs
	for _, app := range desktop.windows() {
		if app == source {
			continue
		}
		app.reconcileSidebar()
		if previous.Appearance != desktop.prefs.Appearance {
			app.applyAppearance()
		}
		if previous.AutoRefreshFiles != desktop.prefs.AutoRefreshFiles ||
			previous.AutoFetchRemotes != desktop.prefs.AutoFetchRemotes ||
			previous.AutoFetchMinutes != desktop.prefs.AutoFetchMinutes || previous.Git != desktop.prefs.Git {
			app.restartRepositoryBackground()
		}
		if previous.LFS != desktop.prefs.LFS {
			app.lfsState = nil
		}
		if app.win != nil {
			app.win.Invalidate()
		}
	}
}

func (desktop *nativeDesktop) remove(app *nativeApp) {
	if desktop.active == app {
		desktop.active = nil
	}
	if desktop.library == app {
		desktop.library = nil
	}
	for path, current := range desktop.repositories {
		if current == app {
			delete(desktop.repositories, path)
		}
	}
}

func (desktop *nativeDesktop) prepareQuit() bool {
	if desktop.checkingQuit {
		return false
	}
	desktop.checkingQuit = true
	defer func() {
		desktop.checkingQuit = false
		for _, app := range desktop.windows() {
			if app.win != nil {
				app.win.Invalidate()
			}
		}
	}()
	// 先检查所有窗口的写操作，再询问未保存内容，避免退出被后面的窗口取消时前面的窗口已经关闭。
	windows := desktop.windows()
	for _, app := range windows {
		if app.win != nil {
			app.win.Invalidate()
		}
	}
	for _, app := range windows {
		if app.closeBlockReason() != "" {
			desktop.present(app)
			app.prepareClose()
			return false
		}
	}
	for _, app := range windows {
		if !app.prepareClose() {
			desktop.present(app)
			return false
		}
	}
	desktop.quitting = true
	return true
}
