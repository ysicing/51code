// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestNativeTrashFirstPromptAndPermanentConfirmation(t *testing.T) {
	app := &nativeApp{prefs: &nativePreferences{}}
	preview := gitrepo.DiscardPreview{Supported: true, Untracked: true, Present: true}
	app.confirmTrashDiscard("scratch.txt", preview)
	tester := ui.NewTester(app.dialogView, 480, 600)
	for _, label := range []string{locales.TrashFirstDelete(), locales.TrashRetention(), locales.TrashRetentionDays("30")} {
		if _, found := tester.Find(label); !found {
			t.Fatalf("missing first-use control %q", label)
		}
	}
	if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
		saveNativeScreenshot(t, tester, directory, "trash-first-use.png")
	}
	tester.Key(0, ui.KeyEscape)
	if app.dialog != nil || app.prefs.Trash.Configured {
		t.Fatal("canceling first use changed the global setting")
	}
	app.prefs.Trash.Disabled = true
	app.confirmTrashDiscard("scratch.txt", preview)
	tester.Frame()
	if _, found := tester.Find(locales.TrashDeletePermanentlyTitle()); !found {
		t.Fatal("disabled Trash did not show a permanent-delete confirmation")
	}
	if _, found := tester.Find(locales.TrashRetention()); found {
		t.Fatal("permanent-delete confirmation still shows retention settings")
	}
}

func TestNativeTrashListAndSettings(t *testing.T) {
	app := &nativeApp{prefs: &nativePreferences{}}
	app.trashDialog([]gitrepo.DiscardBackup{
		{ID: "discard-example", Repository: "/tmp/51Code", Path: "internal/app/actions.go", Present: true, Created: time.Now()},
	}, "")
	tester := ui.NewTester(app.dialogView, 820, 600)
	if _, found := tester.Access(locales.TrashRestore()); !found {
		t.Fatal("回收站列表缺少恢复按钮")
	}
	if _, found := tester.Find("actions.go"); !found {
		t.Fatal("回收站列表缺少文件名")
	}
	if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
		saveNativeScreenshot(t, tester, directory, "trash-list.png")
	}
	settings := ui.NewTester(app.trashPreferences, 460, 220)
	if err := settings.Click(locales.TrashEnabled()); err != nil {
		t.Fatal(err)
	}
	if !app.prefs.Trash.Disabled || !app.prefs.Trash.Configured {
		t.Fatal("设置开关未关闭回收站")
	}
}

func TestNativeBranchConfirmationLayout(t *testing.T) {
	app, path, finish := nativeFixture(t)
	nativeGit(t, path, "config", "--local", "core.longpaths", "true")
	for _, width := range []int{1024, 480} {
		for _, dark := range []bool{false, true} {
			for _, force := range []bool{false, true} {
				name := "codex/windows-ubuntu-x64"
				if width == 480 {
					name = "feature/" + strings.Repeat("long-branch-name/", 15) + "end"
				}
				if nativeGit(t, path, "branch", "--list", name) == "" {
					nativeGit(t, path, "branch", name)
				}
				app.deleteBranch(name, force)
				finish()
				tester := ui.NewTester(app.dialogView, width, 680)
				tester.SetDark(dark)
				dialog := app.dialog
				title, titleFound := tester.Find(dialog.title)
				subject, subjectFound := tester.Find(name)
				detail, detailFound := tester.Find(dialog.detail)
				cancel, cancelFound := tester.AccessByRole(ui.RoleButton, locales.Cancel())
				action, actionFound := tester.AccessByRole(ui.RoleButton, locales.Delete())
				if !titleFound || !subjectFound || !detailFound || !cancelFound || !actionFound {
					t.Fatal("删除确认缺少标题、分支、说明或操作")
				}
				if title.Y+title.H > subject.Y || subject.Y+subject.H > detail.Y ||
					detail.Y+detail.H > action.Bounds.Y {
					t.Fatalf("确认信息与操作重叠: title=%+v subject=%+v detail=%+v action=%+v", title, subject, detail, action)
				}
				if title.X < 0 || action.Bounds.X+action.Bounds.W > float32(width) ||
					action.Bounds.X+action.Bounds.W-title.X > 400 {
					t.Fatal("确认框未保持紧凑或超出窗口")
				}
				if cancel.Bounds.W < 72 || action.Bounds.W < 72 || !tester.Focused(locales.Cancel()) {
					t.Fatal("按钮点击区域或默认取消焦点不正确")
				}
				if force && !strings.Contains(dialog.detail, locales.ForceDeleteBranchDetail()) {
					t.Fatal("强制删除缺少未合并提交警告")
				}
				if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
					saveNativeScreenshot(t, tester, directory, fmt.Sprintf("confirmation-%d-dark-%t-force-%t.png", width, dark, force))
				}
				app.closeDialog(dialog)
			}
		}
	}
}

func TestNativeBranchConfirmationActions(t *testing.T) {
	app, path, finish := nativeFixture(t)
	name := "feature/delete-confirmation"
	nativeGit(t, path, "branch", name)
	for _, key := range []ui.Key{ui.KeyEscape, ui.KeyEnter} {
		app.deleteBranch(name, false)
		finish()
		tester := ui.NewTester(app.dialogView, 800, 600)
		tester.Key(0, key)
		if app.dialog != nil || app.busy || nativeGit(t, path, "branch", "--list", name) == "" {
			t.Fatal("取消或默认回车没有安全关闭确认框")
		}
	}
	app.deleteBranch(name, false)
	finish()
	tester := ui.NewTester(app.dialogView, 800, 600)
	app.busy = true
	tester.Frame()
	for _, label := range []string{locales.Cancel(), locales.Delete()} {
		if button, found := tester.AccessByRole(ui.RoleButton, label); !found || !button.Disabled {
			t.Fatal("操作执行时确认按钮仍可用")
		}
	}
	tester.Key(0, ui.KeyEscape)
	if app.dialog == nil {
		t.Fatal("执行中 Esc 关闭了确认框")
	}
	app.busy = false
	tester.Frame()
	if err := tester.Click(locales.Delete()); err != nil {
		t.Fatal(err)
	}
	finish()
	if app.dialog != nil || app.problem != "" || nativeGit(t, path, "branch", "--list", name) != "" {
		t.Fatalf("明确确认后没有删除临时测试分支: %s", app.problem)
	}
}
