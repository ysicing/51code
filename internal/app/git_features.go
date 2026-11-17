// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"maps"
	"strconv"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

type nativeGitOverride struct {
	LFS       *bool `json:"lfs,omitempty"`
	Checks    *bool `json:"checks,omitempty"`
	Threshold *int  `json:"thresholdMiB,omitempty"`
}

type nativeGitFeatures struct {
	lfs       bool
	checks    bool
	threshold int64
}

func validLargeFileMiB(value int) bool {
	return value > 0 && uint64(value) <= uint64(1<<63-1)>>20
}

func normalizeGitFeatures(prefs *nativePreferences) {
	if !validLargeFileMiB(prefs.LargeFileMiB) {
		prefs.LargeFileMiB = 50
	}
	if prefs.RepositoryGit == nil {
		prefs.RepositoryGit = map[string]nativeGitOverride{}
	}
	for path, override := range prefs.RepositoryGit {
		if override.Threshold != nil && !validLargeFileMiB(*override.Threshold) {
			override.Threshold = nil
			prefs.RepositoryGit[path] = override
		}
		if override == (nativeGitOverride{}) {
			delete(prefs.RepositoryGit, path)
		}
	}
}

func (app *nativeApp) gitFeatures(path string) nativeGitFeatures {
	result := nativeGitFeatures{app.prefs.LFSIntegration, app.prefs.LargeFileChecks, int64(app.prefs.LargeFileMiB) << 20}
	if result.threshold <= 0 {
		result.threshold = 50 << 20
	}
	override := app.prefs.RepositoryGit[path]
	if override.LFS != nil {
		result.lfs = *override.LFS
	}
	if override.Checks != nil {
		result.checks = *override.Checks
	}
	if override.Threshold != nil && validLargeFileMiB(*override.Threshold) {
		result.threshold = int64(*override.Threshold) << 20
	}
	return result
}

func (app *nativeApp) setRepositoryGitOverride(path string, override nativeGitOverride) {
	previous := *app.prefs
	app.prefs.RepositoryGit = maps.Clone(app.prefs.RepositoryGit)
	if app.prefs.RepositoryGit == nil {
		app.prefs.RepositoryGit = map[string]nativeGitOverride{}
	}
	if override == (nativeGitOverride{}) {
		delete(app.prefs.RepositoryGit, path)
	} else {
		app.prefs.RepositoryGit[path] = override
	}
	app.saveBehaviorPreferences(previous)
}

func featureInheritance(global bool) string {
	if global {
		return locales.InheritEnabled()
	}
	return locales.InheritDisabled()
}

func featureOverrideLabel(value *bool, global bool) string {
	if value == nil {
		return featureInheritance(global)
	}
	if *value {
		return locales.FeatureEnabled()
	}
	return locales.FeatureDisabled()
}

func (app *nativeApp) gitFeaturePreferences(c *ui.Context, parent *nativeDialog) {
	settingsSection(c, locales.LargeFileFeatures(), func() {
		previous := *app.prefs
		settingsRow(c, "Git LFS", func() {
			ui.Checkbox(c, &app.prefs.LFSIntegration, locales.EnableLFSIntegration()).OnClick(func() {
				app.saveBehaviorPreferences(previous)
			})
		})
		settingsRow(c, locales.LargeFileChecks(), func() {
			ui.Checkbox(c, &app.prefs.LargeFileChecks, locales.CheckBeforeStaging()).OnClick(func() {
				app.saveBehaviorPreferences(previous)
			})
		})
		settingsRow(c, locales.LargeFileThreshold(), func() {
			ui.MenuButton(c, fmt.Sprintf("%d MiB", app.prefs.LargeFileMiB), func(menu *ui.Menu) {
				app.thresholdMenu(menu, "", parent)
			}).Label(locales.LargeFileThreshold()).Width(210).Justify(ui.SpaceBetween).Disabled(!app.prefs.LargeFileChecks)
		})
	})
	if app.settingsError != "" {
		ui.Text(c, app.settingsError).FontSize(12).TextColor(c.Theme().Danger)
	}
}

func (app *nativeApp) repositoryGitPreferences(c *ui.Context, path string, parent *nativeDialog) {
	settingsSection(c, locales.RepositoryFeatureScope(), func() {
		for _, feature := range []struct {
			label  string
			global bool
			lfs    bool
		}{{"Git LFS", app.prefs.LFSIntegration, true}, {locales.LargeFileChecks(), app.prefs.LargeFileChecks, false}} {
			settingsRow(c, feature.label, func() {
				override := app.prefs.RepositoryGit[path]
				value := override.Checks
				if feature.lfs {
					value = override.LFS
				}
				ui.MenuButton(c, featureOverrideLabel(value, feature.global), func(menu *ui.Menu) {
					for index, label := range []string{featureInheritance(feature.global), locales.FeatureEnabled(), locales.FeatureDisabled()} {
						if menu.Item(label).Checked(value == nil && index == 0 || value != nil && (*value && index == 1 || !*value && index == 2)).Chosen() {
							var choice *bool
							if index != 0 {
								enabled := index == 1
								choice = &enabled
							}
							if feature.lfs {
								override.LFS = choice
							} else {
								override.Checks = choice
							}
							app.setRepositoryGitOverride(path, override)
						}
					}
				}).Label(feature.label).MinWidth(210).Justify(ui.SpaceBetween)
			})
		}
		settingsRow(c, locales.LargeFileThreshold(), func() {
			label := locales.InheritThreshold(strconv.Itoa(app.prefs.LargeFileMiB))
			if value := app.prefs.RepositoryGit[path].Threshold; value != nil {
				label = fmt.Sprintf("%d MiB", *value)
			}
			ui.MenuButton(c, label, func(menu *ui.Menu) {
				app.thresholdMenu(menu, path, parent)
			}).Label(locales.LargeFileThreshold()).MinWidth(210).Justify(ui.SpaceBetween).Disabled(!app.gitFeatures(path).checks)
		})
	})
	if app.settingsError != "" {
		ui.Text(c, app.settingsError).FontSize(12).TextColor(c.Theme().Danger)
	}
	ui.Divider(c)
	app.repositoryLFSView(c, path, parent)
}

func (app *nativeApp) thresholdMenu(menu *ui.Menu, path string, parent *nativeDialog) {
	if path != "" && menu.Item(locales.InheritThreshold(strconv.Itoa(app.prefs.LargeFileMiB))).Chosen() {
		override := app.prefs.RepositoryGit[path]
		override.Threshold = nil
		app.setRepositoryGitOverride(path, override)
	}
	for _, value := range []int{25, 50, 100, 250} {
		if menu.Item(fmt.Sprintf("%d MiB", value)).Chosen() {
			app.saveThreshold(path, value)
		}
	}
	if menu.Item(locales.CustomThreshold()).Chosen() {
		value := app.gitFeatures(path).threshold >> 20
		dialog := &nativeDialog{
			parent: parent, title: locales.LargeFileThreshold(), action: locales.Save(),
			fields: []nativeField{{label: "MiB", value: strconv.FormatInt(value, 10)}},
		}
		dialog.submit = func(dialog *nativeDialog) {
			value, err := strconv.Atoi(dialog.fields[0].value)
			if err != nil || !validLargeFileMiB(value) {
				dialog.problem = locales.PositiveThresholdRequired()
				return
			}
			app.saveThreshold(path, value)
			if app.settingsError != "" {
				dialog.problem = app.settingsError
			} else {
				app.dialog = parent
			}
		}
		app.dialog = dialog
	}
}

func (app *nativeApp) saveThreshold(path string, value int) {
	if !validLargeFileMiB(value) {
		return
	}
	if path != "" {
		override := app.prefs.RepositoryGit[path]
		override.Threshold = &value
		app.setRepositoryGitOverride(path, override)
		return
	}
	previous := *app.prefs
	app.prefs.LargeFileMiB = value
	app.saveBehaviorPreferences(previous)
}
