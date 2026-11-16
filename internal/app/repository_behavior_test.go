// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ysicing/51code/internal/workspace"

	"github.com/egoist/mygo/ui"
)

func TestNativeRepositoryBehaviorDefaultsAndMigration(t *testing.T) {
	base, _, _ := nativeFixture(t)
	for _, data := range []string{`{}`, `{"autoRefreshFiles":false,"autoFetchRemotes":true,"autoFetchMinutes":30,"startupPage":"last"}`} {
		stateFile := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(stateFile, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		app := newNativeApp(base.service, stateFile)
		t.Cleanup(app.cancel)
		if data == "{}" {
			if !app.prefs.AutoRefreshFiles || app.prefs.AutoFetchRemotes || app.prefs.AutoFetchMinutes != 15 ||
				app.prefs.StartupPage != "library" {
				t.Fatal("old preferences did not retain the intended defaults")
			}
		} else if app.prefs.AutoRefreshFiles || !app.prefs.AutoFetchRemotes || app.prefs.AutoFetchMinutes != 30 ||
			app.prefs.StartupPage != "last" {
			t.Fatal("explicit behavior preferences were not preserved")
		}
	}
	prefs := nativePreferences{StartupPage: "unknown", AutoFetchMinutes: -1}
	normalizeNativePreferences(&prefs)
	if prefs.StartupPage != "library" || prefs.AutoFetchMinutes != 15 {
		t.Fatal("unsupported behavior preferences were not normalized")
	}
}

func TestNativeStartupRestoresLastRepositoryAndDraft(t *testing.T) {
	app, _, _ := nativeFixture(t)
	path := app.repository.snapshot.Path
	if app.prefs.LastRepository != path {
		t.Fatal("opening a repository did not remember it")
	}
	app.prefs.StartupPage = "last"
	app.prefs.Drafts[path] = "保留草稿"
	if err := app.savePreferences(); err != nil {
		t.Fatal(err)
	}
	restored := newNativeApp(app.service, app.stateFile)
	t.Cleanup(restored.cancel)
	updates := make(chan func(), 8)
	restored.post = func(update func()) { updates <- update }
	restored.restoreStartupRepository()
	runBackgroundUpdates(t, updates, func() bool { return !restored.busy })
	if restored.screen != "changes" || restored.repository == nil || restored.repository.snapshot.Path != path ||
		restored.prefs.Drafts[path] != "保留草稿" || restored.prefs.Sidebar != app.prefs.Sidebar {
		t.Fatal("startup restoration lost the repository, draft, or layout")
	}
}

func TestNativeStartupFallsBackForRemovedOrMissingRepository(t *testing.T) {
	app, _, _ := nativeFixture(t)
	for _, listed := range []bool{false, true} {
		restored := newNativeApp(app.service, app.stateFile)
		t.Cleanup(restored.cancel)
		restored.prefs.StartupPage = "last"
		restored.prefs.LastRepository = filepath.Join(t.TempDir(), "gone")
		restored.library.Repositories = nil
		if listed {
			restored.library.Repositories = []workspace.LocalRepository{{Path: restored.prefs.LastRepository}}
		}
		updates := make(chan func(), 8)
		restored.post = func(update func()) { updates <- update }
		restored.restoreStartupRepository()
		runBackgroundUpdates(t, updates, func() bool { return !restored.busy })
		if restored.screen != "library" || restored.repository != nil || listed && restored.problem == "" {
			t.Fatal("unavailable startup repository did not fall back to the library")
		}
	}
}

func TestNativeRepositoryBehaviorPreferencesPersistAndRollback(t *testing.T) {
	app, _, _ := nativeFixture(t)
	t.Cleanup(app.stopRepositoryBackground)
	tester := sidebarSettingsTester(t, app)
	clickNativeSettingsControl(t, tester, ui.RoleMenuButton, "启动时")
	if err := tester.ChooseMenuItem("上次打开的仓库"); err != nil {
		t.Fatal(err)
	}
	clickNativeSettingsControl(t, tester, ui.RoleCheckBox, "文件变化时自动刷新")
	clickNativeSettingsControl(t, tester, ui.RoleCheckBox, "定时获取远端更新")
	clickNativeSettingsControl(t, tester, ui.RoleMenuButton, "获取间隔")
	if err := tester.ChooseMenuItem("每 5 分钟"); err != nil {
		t.Fatal(err)
	}
	if app.prefs.StartupPage != "last" || app.prefs.AutoRefreshFiles || !app.prefs.AutoFetchRemotes {
		t.Fatal("behavior controls did not update preferences")
	}
	restored := newNativeApp(app.service, app.stateFile)
	t.Cleanup(restored.cancel)
	if restored.prefs.StartupPage != "last" || restored.prefs.AutoRefreshFiles || !restored.prefs.AutoFetchRemotes ||
		restored.prefs.AutoFetchMinutes != 5 {
		t.Fatal("behavior preferences did not persist")
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	app.stateFile = filepath.Join(blocker, "state.json")
	background := app.background
	clickNativeSettingsControl(t, tester, ui.RoleCheckBox, "文件变化时自动刷新")
	if app.prefs.AutoRefreshFiles || app.settingsError == "" || app.background != background {
		t.Fatal("save failure did not roll back the refresh preference")
	}
	if control, found := tester.AccessByRole(ui.RoleCheckBox, "文件变化时自动刷新"); !found || control.Checked {
		t.Fatal("failed preference save left a misleading checkbox state")
	}
}
