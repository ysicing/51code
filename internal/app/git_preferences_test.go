// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"

	"github.com/egoist/mygo/ui"
)

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
	clickNativeSettingsControl(t, tester, ui.RoleCheckBox, "双击文件暂存或取消暂存")
	clickNativeSettingsControl(t, tester, ui.RoleMenuButton, "历史日期")
	if err := tester.ChooseMenuItem("提交日期"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	restored := newNativeApp(app.service, app.stateFile)
	t.Cleanup(restored.cancel)
	if !restored.prefs.DoubleClickStage || restored.prefs.HistoryDate != "committer" || app.identityEditor.dirty() {
		t.Fatal("Git 应用偏好未即时保存，或错误修改了身份草稿")
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	app.stateFile = filepath.Join(blocker, "state.json")
	clickNativeSettingsControl(t, tester, ui.RoleCheckBox, "双击文件暂存或取消暂存")
	if !app.prefs.DoubleClickStage || app.settingsError == "" || !tester.HasText(app.settingsError) {
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
