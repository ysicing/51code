// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"runtime"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func (app *nativeApp) gitExecutableOptions() gitrepo.ExecutableOptions {
	options := app.prefs.Git
	options.Resources, _ = mygo.App.Path(mygo.PathResources)
	return options
}

func gitSourceLabel(source string) string {
	if source == "" {
		source = "system"
		if runtime.GOOS == "windows" {
			source = "bundled"
		}
	}
	switch source {
	case "bundled":
		return locales.BundledGit()
	case "custom":
		return locales.CustomGit()
	default:
		return locales.SystemGit()
	}
}

func (app *nativeApp) selectGitExecutable(source, path string, editor *nativeIdentityEditor, dialog *nativeDialog) {
	if app.busy || app.closed || editor.dirty() || app.gitToolsBusy() {
		return
	}
	options := app.gitExecutableOptions()
	options.Source, options.Path = source, path
	app.runTask(locales.DetectingGit(), true, func(ctx context.Context) (func(), error) {
		info, err := gitrepo.InspectExecutable(ctx, options)
		if err != nil {
			return nil, err
		}
		return func() {
			if app.dialog != dialog {
				return
			}
			if app.gitToolsBusy() {
				dialog.problem = locales.OperationBusyDetail()
				return
			}
			previous := app.prefs.Git
			app.prefs.Git = gitrepo.ExecutableOptions{Source: source, Path: path}
			if err := app.savePreferences(); err != nil {
				app.prefs.Git = previous
				dialog.problem = locales.SaveSettingsError(err.Error())
				return
			}
			app.stopRepositoryBackground()
			gitrepo.ConfigureExecutable(options)
			editor.gitInfo, editor.gitError, editor.ready = info, "", false
			editor.saved = false
			dialog.problem = ""
			app.restartRepositoryBackground()
			editor.load(app, dialog)
		}, nil
	})
}

func (app *nativeApp) chooseGitExecutable(editor *nativeIdentityEditor, dialog *nativeDialog) {
	if app.busy || app.closed || editor.dirty() || app.gitToolsBusy() {
		return
	}
	app.busy = true
	options := mygo.OpenDialogOptions{Parent: app.win, Title: locales.ChooseGit()}
	if runtime.GOOS == "windows" {
		options.Filters = []mygo.FileFilter{{Name: "Git", Extensions: []string{"exe"}}}
	}
	paths, err := app.openDialog(options)
	app.busy = false
	if err != nil {
		dialog.problem = err.Error()
	} else if len(paths) > 0 {
		app.selectGitExecutable("custom", paths[0], editor, dialog)
	}
}

func (app *nativeApp) gitExecutablePreferences(c *ui.Context, editor *nativeIdentityEditor, dialog *nativeDialog) {
	settingsSection(c, locales.GitEnvironment(), func() {
		settingsRow(c, locales.GitSource(), func() {
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
				ui.MenuButton(c, gitSourceLabel(app.prefs.Git.Source), func(menu *ui.Menu) {
					if runtime.GOOS == "windows" && menu.Item(locales.BundledGit()).Chosen() {
						app.selectGitExecutable("bundled", "", editor, dialog)
					}
					if menu.Item(locales.SystemGit()).Chosen() {
						app.selectGitExecutable("system", "", editor, dialog)
					}
					if menu.Item(locales.ChooseGit()).Chosen() {
						app.chooseGitExecutable(editor, dialog)
					}
				}).Label(locales.GitSource()).Width(210).Justify(ui.SpaceBetween).Disabled(app.busy || editor.dirty() || app.gitToolsBusy())
				if ui.Button(c, locales.DetectGit()).Disabled(app.busy || editor.dirty()).Clicked() {
					editor.load(app, dialog)
				}
			})
			if editor.gitInfo.Version != "" {
				ui.Text(c, editor.gitInfo.Version).FontSize(12).FillWidth()
			}
			path := editor.gitInfo.Path
			if path == "" && app.prefs.Git.Source == "custom" {
				path = app.prefs.Git.Path
			}
			if path != "" {
				ui.Text(c, path).FontSize(11).TextColor(c.Theme().TextMuted).FillWidth().MaxLines(1).Tooltip(path).Selectable()
			}
			if editor.gitError != "" {
				ui.Text(c, locales.GitUnavailable()).FontSize(12).TextColor(c.Theme().Danger).FillWidth().Tooltip(editor.gitError)
			}
		})
	})
	ui.Divider(c)
}
