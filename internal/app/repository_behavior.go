// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"slices"

	"github.com/ysicing/51code/internal/workspace"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func (app *nativeApp) restoreStartupRepository() {
	if app.prefs.StartupPage != "last" || app.prefs.LastRepository == "" || app.closed || app.busy {
		return
	}
	if slices.ContainsFunc(app.library.Repositories, func(entry workspace.LocalRepository) bool {
		return entry.Path == app.prefs.LastRepository
	}) {
		app.openRepository(app.prefs.LastRepository)
	}
}

func (app *nativeApp) saveBehaviorPreferences(previous nativePreferences) {
	app.settingsError = ""
	if err := app.savePreferences(); err != nil {
		*app.prefs = previous
		app.settingsError = locales.SaveSettingsError(err.Error())
		return
	}
	if previous.AutoRefreshFiles != app.prefs.AutoRefreshFiles || previous.AutoFetchRemotes != app.prefs.AutoFetchRemotes ||
		previous.AutoFetchMinutes != app.prefs.AutoFetchMinutes {
		app.restartRepositoryBackground()
	}
}

func fetchIntervalLabel(minutes int) string {
	switch minutes {
	case 5:
		return locales.EveryFiveMinutes()
	case 30:
		return locales.EveryThirtyMinutes()
	default:
		return locales.EveryFifteenMinutes()
	}
}

func (app *nativeApp) repositoryBehaviorPreferences(c *ui.Context) {
	settingsRow(c, locales.StartupAction(), func() {
		label := locales.StartupLibrary()
		if app.prefs.StartupPage == "last" {
			label = locales.StartupLastRepository()
		}
		ui.MenuButton(c, label, func(menu *ui.Menu) {
			for _, choice := range []struct{ value, label string }{
				{"library", locales.StartupLibrary()}, {"last", locales.StartupLastRepository()},
			} {
				if menu.Item(choice.label).Checked(app.prefs.StartupPage == choice.value).Chosen() {
					previous := *app.prefs
					app.prefs.StartupPage = choice.value
					app.saveBehaviorPreferences(previous)
				}
			}
		}).Label(locales.StartupAction()).Width(210).Justify(ui.SpaceBetween)
	})
	ui.Divider(c)
	settingsSection(c, locales.RepositoryBehavior(), func() {
		previous := *app.prefs
		ui.Checkbox(c, &app.prefs.AutoRefreshFiles, locales.AutoRefreshFiles()).OnClick(func() {
			app.saveBehaviorPreferences(previous)
		})
		ui.Row(c).FillWidth().Gap(16).AlignItems(ui.Center).Children(func() {
			ui.Checkbox(c, &app.prefs.AutoFetchRemotes, locales.AutoFetchRemotes()).Grow(1).OnClick(func() {
				app.saveBehaviorPreferences(previous)
			})
			ui.MenuButton(c, fetchIntervalLabel(app.prefs.AutoFetchMinutes), func(menu *ui.Menu) {
				for _, minutes := range []int{5, 15, 30} {
					if menu.Item(fetchIntervalLabel(minutes)).Checked(app.prefs.AutoFetchMinutes == minutes).Chosen() {
						previous := *app.prefs
						app.prefs.AutoFetchMinutes = minutes
						app.saveBehaviorPreferences(previous)
					}
				}
			}).Label(locales.AutoFetchInterval()).Width(140).Justify(ui.SpaceBetween).Disabled(!app.prefs.AutoFetchRemotes)
		})
	})
}
