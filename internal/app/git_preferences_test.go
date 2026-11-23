// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestNativeDoubleClickStageDefaultsAndMigration(t *testing.T) {
	base, _, _ := nativeFixture(t)
	for _, test := range []struct {
		name    string
		data    string
		enabled bool
	}{
		{"new", "", true},
		{"legacy", `{}`, true},
		{"enabled", `{"doubleClickStage":true}`, true},
		{"disabled", `{"doubleClickStage":false}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateFile := filepath.Join(t.TempDir(), "preferences.json")
			if test.data != "" {
				if err := os.WriteFile(stateFile, []byte(test.data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			app := newNativeApp(base.service, stateFile)
			t.Cleanup(app.cancel)
			if app.prefs.DoubleClickStage != test.enabled {
				t.Fatalf("双击暂存 = %t，预期 %t", app.prefs.DoubleClickStage, test.enabled)
			}
			if err := app.savePreferences(); err != nil {
				t.Fatal(err)
			}
			restored := newNativeApp(base.service, stateFile)
			t.Cleanup(restored.cancel)
			if restored.prefs.DoubleClickStage != test.enabled {
				t.Fatal("重启后没有保留双击暂存偏好")
			}
		})
	}
}

func TestNativeDoubleClickStagesWorkingFile(t *testing.T) {
	for _, screen := range []string{"changes", "history"} {
		for _, tree := range []bool{false, true} {
			t.Run(screen+map[bool]string{false: "/list", true: "/tree"}[tree], func(t *testing.T) {
				app, path, finish := nativeFixture(t)
				writeNativeFile(t, path, "hello.txt", "double click\n")
				writeNativeFile(t, path, "other.txt", "leave unstaged\n")
				app.refresh()
				finish()
				app.screen = screen
				app.historyWorking = screen == "history"
				app.prefs.FileTree = tree
				state := filepath.Join(t.TempDir(), "preferences.json")
				if err := os.WriteFile(state, []byte(`{"doubleClickStage":true}`), 0600); err != nil {
					t.Fatal(err)
				}
				restored := newNativeApp(app.service, state)
				t.Cleanup(restored.cancel)
				app.prefs = restored.prefs
				app.prefs.FileTree = tree
				tester := ui.NewTester(app.view, 1024, 680)
				for range 2 {
					if err := tester.Click("hello.txt"); err != nil {
						t.Fatal(err)
					}
				}
				for app.busy || app.readTask != nil {
					finish()
				}
				tester.Frame()
				if got := nativeGit(t, path, "diff", "--cached", "--name-only"); got != "hello.txt" {
					t.Fatalf("双击没有暂存目标文件: %q", got)
				}
				if app.screen != screen || app.selectedFile != "hello.txt" || !app.staged {
					t.Fatal("暂存后未保留页面与文件选择")
				}
				tester.Advance(time.Second)
				for range 2 {
					if err := tester.Click("hello.txt"); err != nil {
						t.Fatal(err)
					}
				}
				for app.busy || app.readTask != nil {
					finish()
				}
				if got := nativeGit(t, path, "diff", "--cached", "--name-only"); got != "" {
					t.Fatalf("双击没有取消暂存: %q", got)
				}
				if app.screen != screen || app.selectedFile != "hello.txt" || app.staged {
					t.Fatal("取消暂存后未保留页面与文件选择")
				}
			})
		}
	}
}

func TestNativeGitPreferencesPersistAndRollback(t *testing.T) {
	app, _, finish := nativeIdentityFixture(t)
	tester := sidebarSettingsTester(t, app)
	if err := tester.Click("Git"); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if checkbox, found := tester.AccessByRole(ui.RoleCheckBox, "换行"); !found || !checkbox.Checked {
		t.Fatal("换行默认应勾选")
	}
	clickNativeSettingsControl(t, tester, ui.RoleCheckBox, "换行")
	if checkbox, found := tester.AccessByRole(ui.RoleCheckBox, "双击文件暂存或取消暂存"); !found || !checkbox.Checked {
		t.Fatal("双击暂存默认应勾选")
	}
	clickNativeSettingsControl(t, tester, ui.RoleCheckBox, "双击文件暂存或取消暂存")
	clickNativeSettingsControl(t, tester, ui.RoleMenuButton, "历史日期")
	if err := tester.ChooseMenuItem("提交日期"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	restored := newNativeApp(app.service, app.stateFile)
	t.Cleanup(restored.cancel)
	if restored.prefs.Wrap || restored.prefs.DoubleClickStage || restored.prefs.HistoryDate != "committer" || app.identityEditor.dirty() {
		t.Fatal("Git 应用偏好未即时保存，或错误修改了身份草稿")
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	app.stateFile = filepath.Join(blocker, "state.json")
	clickNativeSettingsControl(t, tester, ui.RoleCheckBox, "换行")
	if app.prefs.Wrap || app.settingsError == "" || !tester.HasText(app.settingsError) {
		t.Fatal("保存失败时没有恢复换行偏好")
	}
	clickNativeSettingsControl(t, tester, ui.RoleCheckBox, "双击文件暂存或取消暂存")
	if app.prefs.DoubleClickStage || app.settingsError == "" || !tester.HasText(app.settingsError) {
		t.Fatal("保存失败时没有恢复双击偏好")
	}
	clickNativeSettingsControl(t, tester, ui.RoleMenuButton, "历史日期")
	if err := tester.ChooseMenuItem("作者日期"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if app.prefs.HistoryDate != "committer" || app.settingsError == "" || !tester.HasText(app.settingsError) {
		t.Fatal("保存失败时没有恢复历史日期偏好")
	}
}

func TestNativeWrapDefaultsAndMigration(t *testing.T) {
	base, _, _ := nativeFixture(t)
	for _, test := range []struct {
		name, data string
		enabled    bool
	}{
		{"new", "", true},
		{"legacy", `{}`, true},
		{"enabled", `{"wrap":true}`, true},
		{"disabled", `{"wrap":false}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateFile := filepath.Join(t.TempDir(), "preferences.json")
			if test.data != "" {
				if err := os.WriteFile(stateFile, []byte(test.data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			app := newNativeApp(base.service, stateFile)
			t.Cleanup(app.cancel)
			if app.prefs.Wrap != test.enabled {
				t.Fatalf("换行 = %t，预期 %t", app.prefs.Wrap, test.enabled)
			}
		})
	}
}

func TestNativeWrapSettingsPersistAcrossWindows(t *testing.T) {
	desktop, path, updates, _ := desktopFixture(t)
	app := openDesktopRepository(t, desktop, updates, path)
	other := desktop.library
	app.setDiff(gitrepo.Diff{
		Text: "diff --git a/hello.txt b/hello.txt\n--- a/hello.txt\n+++ b/hello.txt\n@@ -1 +1 @@\n-old\n+new\n",
		Old:  &gitrepo.FileContent{Path: "hello.txt", Present: true, Text: "old\n"},
		New:  &gitrepo.FileContent{Path: "hello.txt", Present: true, Text: "new\n"},
	})
	for _, full := range []bool{false, true} {
		app.fileViewer.full = full
		tester := ui.NewTester(app.fileDiffPanel, 800, 500)
		previous := app.prefs.Wrap
		settings := ui.NewTester(app.gitPreferences, 800, 500)
		if err := settings.Click("换行"); err != nil {
			t.Fatal(err)
		}
		settings.Frame()
		tester.Frame()
		if _, found := tester.AccessByRole(ui.RoleCheckBox, "换行"); found {
			t.Fatal("文件查看工具栏不应再显示换行开关")
		}
		restored := newNativeApp(app.service, app.stateFile)
		t.Cleanup(restored.cancel)
		if app.prefs.Wrap == previous || other.prefs.Wrap != app.prefs.Wrap ||
			desktop.applied.Wrap != app.prefs.Wrap || restored.prefs.Wrap != app.prefs.Wrap {
			t.Fatal("全局换行偏好未保存或同步其他窗口")
		}
		if full {
			if app.fileViewer.states[app.fileViewer.side].SoftWrap != app.prefs.Wrap {
				t.Fatal("全文预览未应用换行偏好")
			}
		} else if len(app.diffViews) == 0 || app.diffViews[0].Wrap != app.prefs.Wrap {
			t.Fatal("Diff 未应用换行偏好")
		}
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	app.stateFile = filepath.Join(blocker, "state.json")
	previous := app.prefs.Wrap
	tester := ui.NewTester(app.gitPreferences, 800, 500)
	if err := tester.Click("换行"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if app.prefs.Wrap != previous || other.prefs.Wrap != previous || app.settingsError == "" || app.status != app.settingsError {
		t.Fatal("全局设置保存失败没有回滚或提示错误")
	}
}

func TestNativeGitignoreTemplateEditor(t *testing.T) {
	app, _, _ := nativeIdentityFixture(t)
	tester := sidebarSettingsTester(t, app)
	if err := tester.Click(locales.TemplateSettings()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	settings := app.dialog
	click := func(label string) {
		t.Helper()
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	focusTemplate := func() {
		t.Helper()
		field, found := tester.AccessByRole(ui.RoleTextField, locales.GitignoreTemplate())
		if !found || field.Offscreen {
			t.Fatal("missing template text field")
		}
		tester.ClickAt(field.Bounds.X+field.Bounds.W/2, field.Bounds.Y+field.Bounds.H/2)
	}
	clickNativeSettingsControl(t, tester, ui.RoleButton, locales.Edit())
	editor := app.dialog
	if editor == settings || app.prefs.GitignoreTemplate != defaultGitignoreTemplate {
		t.Fatal("expected editor with the built-in template")
	}
	focusTemplate()
	tester.Key(ui.Cmd, ui.KeyA)
	tester.Type("*.local\n.cache/\n")
	if !editor.canSubmit() {
		t.Fatal("template editor did not accept changes")
	}
	tester.Key(0, ui.KeyEscape)
	if app.dialog == editor || app.dialog.parent != editor {
		t.Fatal("closing the editor should confirm discarding the draft")
	}
	tester.Key(0, ui.KeyEscape)
	if app.dialog != editor {
		t.Fatal("cancelling the discard should reopen the draft")
	}
	click(locales.Save())
	if app.dialog != settings || app.prefs.GitignoreTemplate != "*.local\n.cache/\n" {
		t.Fatalf("customized template was not saved: %q", app.prefs.GitignoreTemplate)
	}
	clickNativeSettingsControl(t, tester, ui.RoleButton, locales.Edit())
	click(locales.RestoreGitignoreTemplate())
	click(locales.Save())
	if app.prefs.GitignoreTemplate != defaultGitignoreTemplate {
		t.Fatal("default template was not restored")
	}
	clickNativeSettingsControl(t, tester, ui.RoleButton, locales.Edit())
	focusTemplate()
	tester.Key(ui.Cmd, ui.KeyA)
	tester.Key(0, ui.KeyBackspace)
	click(locales.Save())
	restored := newNativeApp(app.service, app.stateFile)
	t.Cleanup(restored.cancel)
	if restored.prefs.GitignoreTemplate != "" {
		t.Fatal("explicitly empty template did not survive reload")
	}
	clickNativeSettingsControl(t, tester, ui.RoleButton, locales.Edit())
	editor = app.dialog
	click(locales.RestoreGitignoreTemplate())
	stateFile := app.stateFile
	app.stateFile = filepath.Join(stateFile, "unwritable.json")
	click(locales.Save())
	if app.dialog != editor || editor.problem == "" || !editor.canSubmit() || app.prefs.GitignoreTemplate != "" {
		t.Fatal("failed save should retain the draft and leave the saved template intact")
	}
	app.stateFile = stateFile
	click(locales.Save())
	if app.dialog != settings || app.prefs.GitignoreTemplate != defaultGitignoreTemplate {
		t.Fatal("retry did not save the retained draft")
	}
}

func TestNativeHistoryDatePreferenceUpdatesVisibleHistory(t *testing.T) {
	app, _, _ := nativeFixture(t)
	commit := gitrepo.Commit{
		Hash: app.repository.snapshot.Head, Subject: "different dates", Author: "Native Test",
		Date: "2026-10-01T10:00:00Z", CommitterDate: "2026-11-10T12:00:00Z",
	}
	app.screen, app.commitHash, app.history = "history", commit.Hash, []gitrepo.Commit{commit}
	tester := ui.NewTester(app.view, 1024, 680)
	if !tester.HasText("作者日期") || !tester.HasText(historyDate(commit.Date)) {
		t.Fatal("默认未显示作者日期")
	}
	app.prefs.HistoryDate = "committer"
	tester.Frame()
	if !tester.HasText("提交日期") || !tester.HasText(historyDate(commit.CommitterDate)) ||
		tester.HasText(historyDate(commit.Date)) {
		t.Fatal("列表或详情未同步切换提交日期")
	}
	app.history[0].CommitterDate = ""
	tester.Frame()
	if !tester.HasText(historyDate(commit.Date)) {
		t.Fatal("旧提交数据缺少提交日期时未回退")
	}
}

func TestNativeDoubleClickRespectsWriteGuards(t *testing.T) {
	for _, guard := range []string{"readOnly", "busy", "conflict", "dialog", "committed", "readingHistory"} {
		t.Run(guard, func(t *testing.T) {
			app, _, _ := nativeFixture(t)
			app.prefs.DoubleClickStage = true
			file := gitrepo.File{Path: "hello.txt"}
			switch guard {
			case "readOnly":
				app.writable = false
			case "busy":
				app.busy = true
			case "conflict":
				file.Conflict = true
			case "dialog":
				app.dialog = &nativeDialog{}
			case "committed":
				app.screen, app.historyWorking = "history", false
			case "readingHistory":
				app.readTask = &nativeRead{kind: nativeReadHistory}
			}
			app.stageDoubleClickedFile(file, false)
			if app.readOnlyBusy || app.status != "" {
				t.Fatal("不可写或受限状态启动了暂存操作")
			}
			app.busy, app.readTask = false, nil
		})
	}
}

func TestNativeDoubleClickDisabledByDefault(t *testing.T) {
	app, path, finish := nativeFixture(t)
	writeNativeFile(t, path, "hello.txt", "keep unstaged\n")
	app.refresh()
	finish()
	tester := ui.NewTester(app.view, 1024, 680)
	for range 2 {
		if err := tester.Click("hello.txt"); err != nil {
			t.Fatal(err)
		}
	}
	finish()
	if got := nativeGit(t, path, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("默认双击不应修改暂存区: %q", got)
	}
}
