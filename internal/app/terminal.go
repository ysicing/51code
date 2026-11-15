// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ysicing/51code/internal/terminal"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func (app *nativeApp) directoryDialogPath() string {
	for _, path := range []string{app.prefs.LastDirectory, userHomeDirectory()} {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
	}
	return ""
}

func userHomeDirectory() string {
	path, _ := os.UserHomeDir()
	return path
}

func systemTerminalLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return locales.Terminal()
	case "windows":
		return locales.CommandPrompt()
	default:
		return locales.SystemTerminal()
	}
}

func (app *nativeApp) terminalApplicationLabel() string {
	switch app.prefs.TerminalApplication {
	case "":
		return locales.SystemDefault()
	case terminal.System:
		return systemTerminalLabel()
	default:
		for _, application := range app.terminalApplications {
			if terminalApplicationPath(application.path) == terminalApplicationPath(app.prefs.TerminalApplication) {
				return application.name
			}
		}
		name := filepath.Base(app.prefs.TerminalApplication)
		return strings.TrimSuffix(name, filepath.Ext(name))
	}
}

func (app *nativeApp) setTerminalApplication(application string) {
	if err := terminal.Validate(application); err != nil {
		app.settingsError = locales.TerminalSelectionError(err.Error())
		return
	}
	previous := app.prefs.TerminalApplication
	app.prefs.TerminalApplication = application
	app.settingsError = ""
	if err := app.savePreferences(); err != nil {
		app.prefs.TerminalApplication = previous
		app.settingsError = locales.SaveSettingsError(err.Error())
	}
}

func (app *nativeApp) terminalMenu(menu *ui.Menu) {
	if menu.Item(locales.SystemDefault()).Checked(app.prefs.TerminalApplication == "").Chosen() {
		app.setTerminalApplication("")
	}
	if menu.Item(systemTerminalLabel()).Checked(app.prefs.TerminalApplication == terminal.System).Chosen() {
		app.setTerminalApplication(terminal.System)
	}
	selected := app.prefs.TerminalApplication
	found := false
	for _, application := range app.terminalApplications {
		checked := selected != "" && selected != terminal.System &&
			terminalApplicationPath(selected) == terminalApplicationPath(application.path)
		found = found || checked
		if menu.Item(application.name).Checked(checked).Chosen() {
			app.setTerminalApplication(application.path)
		}
	}
	if !found && selected != "" && selected != terminal.System {
		menu.Item(app.terminalApplicationLabel()).Checked(true).Disabled(true)
	}
	menu.Separator()
	if menu.Item(locales.ChooseOtherApplication()).Chosen() {
		app.chooseTerminalApplication()
	}
}

func (app *nativeApp) chooseTerminalApplication() {
	if app.busy || app.closed {
		return
	}
	app.busy = true
	defer func() { app.busy = false }()
	options := mygo.OpenDialogOptions{Parent: app.win, Title: locales.ChooseTerminalApplication()}
	switch runtime.GOOS {
	case "darwin":
		options.DefaultPath = "/Applications"
		// app 扩展名可能映射到旧 application-file 类型，应用包统一在返回后校验。
	case "windows":
		options.Filters = []mygo.FileFilter{{Name: locales.ApplicationFiles(), Extensions: []string{"exe"}}}
	}
	paths, err := app.openDialog(options)
	if err != nil {
		app.settingsError = locales.TerminalSelectionError(err.Error())
		return
	}
	if len(paths) > 0 {
		app.setTerminalApplication(paths[0])
	}
}

func (app *nativeApp) openRepositoryTerminal(path string) {
	if app.busy || app.closed || app.dialog != nil {
		return
	}
	application := app.prefs.TerminalApplication
	app.runTask(locales.OpeningTerminal(), true, func(_ context.Context) (func(), error) {
		done, err := terminal.Start(path, application)
		if err != nil {
			return nil, fmt.Errorf("%s", locales.TerminalError(err.Error()))
		}
		go func() {
			if err := <-done; err != nil {
				app.post(func() {
					if !app.closed && app.problem == "" {
						app.problem = locales.TerminalError(err.Error())
					}
				})
			}
		}()
		return nil, nil
	})
}
