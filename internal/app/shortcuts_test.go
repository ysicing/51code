// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
)

func shortcutSettingsTester(t *testing.T) (*nativeApp, *ui.Tester) {
	t.Helper()
	mygo.App.SetPath(mygo.PathUserData, t.TempDir())
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })
	settingsTestUpdater.Do(func() {
		mygo.Use(native.New(updater.Options{DisableAutomaticChecks: true, Language: "zh-Hans"}))
	})
	defaults := defaultNativeShortcuts()
	var items []*mygo.MenuItem
	for id, value := range defaults.bindings() {
		items = append(items, &mygo.MenuItem{ID: id, Label: id, Accelerator: *value})
	}
	previousMenu := mygo.App.Menu()
	mygo.App.SetMenu(mygo.NewMenu(items))
	t.Cleanup(func() { mygo.App.SetMenu(previousMenu) })
	app := &nativeApp{
		prefs:     &nativePreferences{Shortcuts: defaults},
		stateFile: filepath.Join(t.TempDir(), "native-state.json"),
	}
	app.settingsDialogAt(2)
	return app, ui.NewTester(app.dialogView, 1024, 680)
}

func checkShortcutMenu(t *testing.T, shortcuts nativeShortcuts) {
	t.Helper()
	for id, value := range shortcuts.bindings() {
		item := mygo.App.Menu().ItemByID(id)
		if item == nil || item.Accelerator != *value {
			t.Fatalf("menu item %s = %+v, want accelerator %q", id, item, *value)
		}
	}
}

func TestNativeShortcutRecordingPersistsAndUpdatesMenu(t *testing.T) {
	app, tester := shortcutSettingsTester(t)
	label := locales.ShortcutLabel(locales.QuickOpen())
	clickNativeSettingsControl(t, tester, ui.RoleButton, label)
	checkShortcutMenu(t, nativeShortcuts{})
	if _, found := tester.Find(locales.RecordShortcut()); !found {
		t.Fatal("录制控件没有显示提示")
	}
	tester.KeyAt(ui.Cmd|ui.Alt, ui.Key9)
	want := defaultNativeShortcuts()
	want.QuickOpen = "CmdOrCtrl+Alt+9"
	if app.prefs.Shortcuts != want || app.shortcutRecording != nil || app.settingsError != "" {
		t.Fatalf("录制状态 = %v, 偏好 = %+v, 错误 = %q", app.shortcutRecording, app.prefs.Shortcuts, app.settingsError)
	}
	checkShortcutMenu(t, want)
	data, err := os.ReadFile(app.stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var saved nativePreferences
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Shortcuts != want {
		t.Fatalf("重读设置得到 %+v", saved.Shortcuts)
	}
	if _, found := tester.Find(shortcutLabel(want.QuickOpen)); !found {
		t.Fatal("录制后没有显示新组合")
	}

	clickNativeSettingsControl(t, tester, ui.RoleButton, locales.RestoreShortcuts())
	checkShortcutMenu(t, defaultNativeShortcuts())
	if app.prefs.Shortcuts != defaultNativeShortcuts() {
		t.Fatalf("恢复默认后得到 %+v", app.prefs.Shortcuts)
	}
}

func TestNativeShortcutRecordingCancels(t *testing.T) {
	for _, scenario := range []struct {
		name string
		stop func(*ui.Tester)
	}{
		{"escape", func(tester *ui.Tester) { tester.Key(0, ui.KeyEscape) }},
		{"tab", func(tester *ui.Tester) { tester.Key(0, ui.KeyTab) }},
		{"blur", func(tester *ui.Tester) { tester.SetFocused(false) }},
		{"outside", func(tester *ui.Tester) {
			label, _ := tester.Find(locales.ScanFolders())
			tester.ClickAt(label.X+label.W/2, label.Y+label.H/2)
		}},
		{"close settings", func(tester *ui.Tester) { _ = tester.Click(locales.CloseSettings()) }},
		{"click backdrop", func(tester *ui.Tester) { tester.ClickAt(5, 5) }},
		{"change category", func(tester *ui.Tester) { _ = tester.Click(locales.General()) }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			app, tester := shortcutSettingsTester(t)
			label := locales.ShortcutLabel(locales.LocalRepositories())
			clickNativeSettingsControl(t, tester, ui.RoleButton, label)
			checkShortcutMenu(t, nativeShortcuts{})
			scenario.stop(tester)
			checkShortcutMenu(t, defaultNativeShortcuts())
			if app.shortcutRecording != nil || app.prefs.Shortcuts != defaultNativeShortcuts() {
				t.Fatalf("取消后的录制状态 = %v, 偏好 = %+v", app.shortcutRecording, app.prefs.Shortcuts)
			}
			if (app.dialog == nil) != (scenario.name == "close settings" || scenario.name == "click backdrop") {
				t.Fatal("设置面板关闭状态不符合预期")
			}
			if _, err := os.Stat(app.stateFile); !os.IsNotExist(err) {
				t.Fatalf("取消录制后设置文件状态 = %v", err)
			}
		})
	}
}

func TestNativeShortcutRecordingKeyboardActivation(t *testing.T) {
	for _, activation := range []ui.Key{ui.KeyEnter, ui.KeySpace} {
		app, tester := shortcutSettingsTester(t)
		label := locales.ShortcutLabel(locales.LocalRepositories())
		clickNativeSettingsControl(t, tester, ui.RoleButton, label)
		tester.Key(0, ui.KeyEscape)
		tester.Key(0, activation)
		if app.shortcutRecording != &app.prefs.Shortcuts.Library {
			t.Fatalf("按 %v 后未开始录制", activation)
		}
		modifier := ui.CodeControlLeft
		if runtime.GOOS == "darwin" {
			modifier = ui.CodeSuperLeft
		}
		tester.CodeDown(ui.Cmd, modifier)
		if _, found := tester.Find(strings.TrimSuffix(shortcutLabel("CmdOrCtrl+"), "+")); !found {
			t.Fatal("录制控件没有实时显示修饰键")
		}
		tester.KeyRepeat(ui.Cmd, ui.KeyK)
		tester.CodeUp(0, modifier)
		if app.shortcutRecording != &app.prefs.Shortcuts.Library || app.prefs.Shortcuts != defaultNativeShortcuts() {
			t.Fatal("修饰键或重复按键结束了录制")
		}
		checkShortcutMenu(t, nativeShortcuts{})
		clickNativeSettingsControl(t, tester, ui.RoleButton, locales.ShortcutLabel(locales.QuickOpen()))
		if app.shortcutRecording != &app.prefs.Shortcuts.QuickOpen {
			t.Fatal("点击另一项后没有切换录制控件")
		}
		checkShortcutMenu(t, nativeShortcuts{})
		tester.Key(0, ui.KeyEscape)
	}
}

func TestNativeShortcutRecordingRejectsInvalidKeys(t *testing.T) {
	unsupported := locales.UnsupportedShortcut(strings.TrimSuffix(shortcutLabel("CmdOrCtrl+"), "+"))
	for _, attempt := range []struct {
		name     string
		mods     ui.Modifiers
		key      ui.Key
		expected string
	}{
		{"menu", ui.Cmd, ui.KeyComma, locales.ReservedShortcut()},
		{"text find", ui.Cmd | ui.Shift, ui.KeyG, locales.ReservedShortcut()},
		{"editor", ui.Cmd, ui.KeySlash, locales.ReservedShortcut()},
		{"duplicate", ui.Cmd, ui.KeyP, locales.DuplicateShortcuts()},
		{"missing modifier", 0, ui.KeyK, unsupported},
		{"unsupported key", ui.Cmd, ui.KeyDown, unsupported},
		{"extra modifier", ui.Ctrl | ui.Super, ui.KeyK, unsupported},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			app, tester := shortcutSettingsTester(t)
			clickNativeSettingsControl(t, tester, ui.RoleButton, locales.ShortcutLabel(locales.LocalRepositories()))
			tester.Key(attempt.mods, attempt.key)
			if app.settingsError != attempt.expected || app.shortcutRecording != nil || app.prefs.Shortcuts != defaultNativeShortcuts() {
				t.Fatalf("错误 = %q, 偏好 = %+v, 录制状态 = %v", app.settingsError, app.prefs.Shortcuts, app.shortcutRecording)
			}
			if _, found := tester.Find(attempt.expected); !found {
				t.Fatalf("设置面板未显示错误 %q", attempt.expected)
			}
			checkShortcutMenu(t, defaultNativeShortcuts())
		})
	}
}

func TestNativeShortcutMigration(t *testing.T) {
	for _, legacy := range []nativeShortcuts{
		{Library: "⌘⇧L", Refresh: "⌥⌘R", Commit: "⌘↩", QuickOpen: "⌘⇧P"},
		{Library: "Ctrl+Shift+L", Refresh: "Alt+Ctrl+R", Commit: "Ctrl+Enter", QuickOpen: "Ctrl+Shift+P"},
	} {
		want := defaultNativeShortcuts()
		want.Refresh, want.QuickOpen = "CmdOrCtrl+Alt+R", "CmdOrCtrl+Shift+P"
		if got := normalizeNativeShortcuts(legacy); got != want || normalizeNativeShortcuts(got) != want {
			t.Fatalf("迁移 %+v 得到 %+v，期望 %+v", legacy, got, want)
		}
	}
	// 旧版下拉框里与编辑器或网页重载重叠的选项，升级后仍需保留。
	overlapping := nativeShortcuts{Library: "⌥⌘L", Refresh: "⌘⇧R", Commit: "⌘⇧↩", QuickOpen: "⌘↩", Scan: "⌘⇧L", AddRepository: "⌘O"}
	want := nativeShortcuts{
		Library: "CmdOrCtrl+Alt+L", Refresh: "CmdOrCtrl+Shift+R", Commit: "CmdOrCtrl+Shift+Enter",
		QuickOpen: "CmdOrCtrl+Enter", Scan: "CmdOrCtrl+Shift+L", AddRepository: "CmdOrCtrl+O",
	}
	if got := normalizeNativeShortcuts(overlapping); got != want {
		t.Fatalf("旧版可选组合迁移得到 %+v，期望 %+v", got, want)
	}
	for _, invalid := range []string{"CmdOrCtrl+Home", "CmdOrCtrl+Shift", "⌘,", "Ctrl+/", "Ctrl+Alt+R", "CmdOrCtrl+Ctrl+K"} {
		shortcuts := defaultNativeShortcuts()
		shortcuts.Refresh, shortcuts.QuickOpen = "⌥⌘R", invalid
		if got := normalizeNativeShortcuts(shortcuts); got != defaultNativeShortcuts() {
			t.Fatalf("非法、保留或重复组合 %q 没有恢复默认值：%+v", invalid, got)
		}
	}
}

func TestNativeShortcutRecordingRestoresMenuOnSaveFailure(t *testing.T) {
	app, tester := shortcutSettingsTester(t)
	app.stateFile = t.TempDir()
	clickNativeSettingsControl(t, tester, ui.RoleButton, locales.ShortcutLabel(locales.QuickOpen()))
	tester.Key(ui.Cmd, ui.KeyF12)
	if app.settingsError == "" || app.shortcutRecording != nil || app.prefs.Shortcuts != defaultNativeShortcuts() {
		t.Fatalf("保存失败后错误 = %q, 偏好 = %+v, 录制状态 = %v", app.settingsError, app.prefs.Shortcuts, app.shortcutRecording)
	}
	checkShortcutMenu(t, defaultNativeShortcuts())
}
