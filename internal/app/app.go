// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"log"
	"path/filepath"
	"runtime"

	"github.com/ysicing/51code/internal/workspace"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
)

// Run 启动原生应用，并接收根入口通过构建参数注入的信息。
func Run(info BuildInfo) {
	buildNumber, buildCommit, buildTime = info.Number, info.Commit, info.Time
	app := mygo.App
	displayName := app.Name()
	if err := preserveLegacyApplicationIdentity(app); err != nil {
		log.Fatal(err)
	}
	if !app.RequestSingleInstanceLock() {
		return
	}
	app.SetName(displayName)
	dir, err := app.Path(mygo.PathUserData)
	if err != nil {
		log.Fatal(err)
	}
	manager, err := workspace.New(filepath.Join(dir, "recent.json"))
	if err != nil {
		log.Fatal(err)
	}
	service := &RepositoryService{manager: manager}
	desktop := newNativeDesktop(service, filepath.Join(dir, "native-state.json"))
	stopGops := startGopsAgent()
	app.OnQuit(stopGops)
	mygo.Use(native.New(updater.Options{
		// 不能自更新的安装（deb/rpm、开发构建）由插件自行跳过后台检查。
		Interval: nativeUpdateCheckInterval(desktop.prefs.UpdateCheckFrequency),
	}))
	open := func() {
		if desktop.checkingQuit || desktop.quitting {
			return
		}
		desktop.showLibrary()
		applyNativeShortcuts(desktop.prefs.Shortcuts)
	}
	command := func(action func(*nativeApp)) func(*mygo.MenuItem, *mygo.Window) {
		return func(_ *mygo.MenuItem, win *mygo.Window) {
			if desktop.checkingQuit || desktop.quitting {
				return
			}
			target := desktop.target(win)
			if target == nil {
				target = desktop.showLibrary()
			}
			target.win.Update(func() {
				if !target.closed && !target.busy && target.dialog == nil {
					action(target)
				}
			})
		}
	}
	libraryCommand := func(action func(*nativeApp)) func(*mygo.MenuItem, *mygo.Window) {
		return func(_ *mygo.MenuItem, _ *mygo.Window) {
			if desktop.checkingQuit || desktop.quitting {
				return
			}
			target := desktop.showLibrary()
			target.win.Update(func() {
				if !target.closed && !target.busy && target.dialog == nil {
					action(target)
				}
			})
		}
	}
	app.OnSecondInstance(func(_ []string, _ string) { open() })
	app.OnBeforeQuit(func(event *mygo.QuitEvent) {
		if event.SessionEnding {
			desktop.quitting = true
			for _, window := range desktop.windows() {
				if err := window.savePreferences(); err != nil {
					log.Printf("保存窗口状态：%v", err)
				}
			}
			return
		}
		if !desktop.prepareQuit() {
			event.PreventDefault()
		}
	})
	app.OnQuitCanceled(func() { desktop.quitting = false })
	setMenu := func() {
		updates := &mygo.MenuItem{
			Label: locales.DownloadRelease(),
			Click: command(func(desktop *nativeApp) {
				if err := mygo.Shell.OpenExternal(desktopReleasesURL); err != nil {
					desktop.problem = err.Error()
				}
			}),
		}
		if selfUpdates() {
			updates = updater.MenuItem()
			updates.Label = locales.CheckUpdates()
		}
		app.SetMenu(mygo.NewMenu(platformMenu([]*mygo.MenuItem{
			{Label: "51Code", Submenu: []*mygo.MenuItem{
				{Label: locales.About(), Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
					showNativeAbout()
				}},
				updates,
				{ID: "settings", Label: locales.SettingsMenu(), Accelerator: "CmdOrCtrl+,", Click: command(func(desktop *nativeApp) { desktop.settingsDialog() })},
				mygo.Separator(),
				{Role: mygo.RoleServices},
				mygo.Separator(),
				{Role: mygo.RoleHide},
				{Role: mygo.RoleHideOthers},
				{Role: mygo.RoleUnhide},
				mygo.Separator(),
				{Role: mygo.RoleQuit},
			}},
			{Label: locales.FilesMenu(), Submenu: []*mygo.MenuItem{
				{ID: shortcutLibrary, Label: locales.LocalRepositories(), Accelerator: shortcutAccelerator(defaultNativeShortcuts().Library), Click: func(_ *mygo.MenuItem, _ *mygo.Window) { open() }},
				{ID: shortcutAddRepo, Label: locales.AddExistingRepositoryMenu(), Accelerator: shortcutAccelerator(defaultNativeShortcuts().AddRepository), Click: libraryCommand(func(desktop *nativeApp) { desktop.chooseLocal() })},
				{ID: shortcutScan, Label: locales.ScanFoldersMenu(), Accelerator: shortcutAccelerator(defaultNativeShortcuts().Scan), Click: libraryCommand(func(desktop *nativeApp) { desktop.chooseScan() })},
				{Label: locales.NewGroupMenu(), Click: libraryCommand(func(desktop *nativeApp) { desktop.groupDialog("") })},
				mygo.Separator(),
				{Label: locales.TrashMenu(), Click: command(func(window *nativeApp) { window.showTrash("") })},
				mygo.Separator(),
				{Role: mygo.RoleClose},
			}},
			{Role: mygo.RoleEditMenu},
			{Label: locales.Repository(), Submenu: []*mygo.MenuItem{
				{ID: shortcutRefresh, Label: locales.Refresh(), Accelerator: shortcutAccelerator(defaultNativeShortcuts().Refresh), Click: command(func(desktop *nativeApp) { desktop.refresh() })},
				{ID: shortcutCommit, Label: locales.Commit(), Accelerator: shortcutAccelerator(defaultNativeShortcuts().Commit), Click: command(func(desktop *nativeApp) { desktop.commit() })},
				mygo.Separator(),
				{Label: locales.RepositorySettingsMenu(), Click: command(func(desktop *nativeApp) { desktop.repositorySettingsDialog() })},
			}},
			{Role: mygo.RoleViewMenu},
			{Role: mygo.RoleWindowMenu},
		}, runtime.GOOS)))
		applyNativeShortcuts(desktop.prefs.Shortcuts)
	}
	app.WhenReady(func() {
		setMenu()
		desktop.startTrashCleanup()
		app.OnLocaleChanged(func(_ string) { setMenu() })
		open()
		desktop.library.restoreStartupRepository()
	})
	if runtime.GOOS == "darwin" {
		app.OnWindowAllClosed(func() {})
	}
	app.OnActivate(func(visible bool) {
		if !visible {
			open()
		}
	})
	err = app.Run()
	stopGops()
	if err != nil {
		log.Fatal(err)
	}
}
