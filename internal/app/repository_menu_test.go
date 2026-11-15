// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeRepositoryEntryMenu(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.screen = "library"
	label := "管理 " + app.library.Repositories[0].Name
	tester := ui.NewTester(app.view, 960, 640)
	if _, found := tester.AccessByRole(ui.RoleMenuButton, label); !found {
		t.Fatal("仓库操作缺少可访问的菜单入口")
	}
	if err := tester.Click(label); err != nil {
		t.Fatal(err)
	}
	want := []string{"在终端中打开", revealRepositoryLabel(), "移动到分组", "-", "从列表移除…"}
	if app.dialog != nil || !slices.Equal(tester.Menu(), want) {
		t.Fatalf("仓库操作未使用精简原生菜单: %v, %+v", tester.Menu(), app.dialog)
	}
	tester.CloseMenu()
	for _, key := range []ui.Key{ui.KeyEnter, ui.KeySpace, ui.KeyDown} {
		tester.Key(0, key)
		if !slices.Equal(tester.Menu(), want) {
			t.Fatalf("键盘 %v 无法打开仓库菜单: %v", key, tester.Menu())
		}
		tester.CloseMenu()
	}
	app.busy = true
	tester.Frame()
	if button, found := tester.AccessByRole(ui.RoleMenuButton, label); !found || !button.Disabled {
		t.Fatal("后台任务执行时仓库菜单未禁用")
	}
	if err := tester.Click(label); err != nil {
		t.Fatal(err)
	}
	if tester.Menu() != nil || app.dialog != nil {
		t.Fatal("忙碌期间仍能打开仓库操作")
	}
}

func TestNativeRepositoryEntryMenuMove(t *testing.T) {
	app, _, finish := nativeFixture(t)
	path := app.repository.snapshot.Path
	library, err := app.service.CreateGroup("日常开发")
	if err != nil {
		t.Fatal(err)
	}
	app.library, app.screen = library, "library"
	label := "管理 " + app.library.Repositories[0].Name
	tester := ui.NewTester(app.view, 960, 640)
	for _, group := range []string{"日常开发", "未分组"} {
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
		if err := tester.ChooseMenuItem("移动到分组", group); err != nil {
			t.Fatal(err)
		}
		finish()
		tester.Frame()
		want := ""
		if group == "日常开发" {
			want = library.Groups[0].ID
		}
		stored := app.service.Library().Repositories[0]
		if stored.Path != path || stored.GroupID != want || app.library.Repositories[0] != stored ||
			app.dialog != nil || app.problem != "" {
			t.Fatalf("菜单移动未立即保存或同步列表: %+v, %s", stored, app.problem)
		}
	}
	if err := tester.Click(label); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("移动到分组", "未分组"); err != nil {
		t.Fatal(err)
	}
	if app.busy {
		t.Fatal("选择当前分组仍发起了后台写入")
	}
}

func TestNativeRepositoryEntryMenuRemove(t *testing.T) {
	app, path, finish := nativeFixture(t)
	app.screen = "library"
	label := "管理 " + app.library.Repositories[0].Name
	tester := ui.NewTester(app.view, 960, 640)
	for _, confirm := range []bool{false, true} {
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
		if err := tester.ChooseMenuItem("从列表移除…"); err != nil {
			t.Fatal(err)
		}
		if app.dialog == nil || app.dialog.action != "移除" ||
			len(app.service.Library().Repositories) != 1 || app.busy {
			t.Fatal("移除记录未先显示确认")
		}
		action := "取消"
		if confirm {
			action = "移除"
		}
		if err := tester.Click(action); err != nil {
			t.Fatal(err)
		}
		if confirm {
			finish()
		}
		tester.Frame()
		want := 1
		if confirm {
			want = 0
		}
		if len(app.service.Library().Repositories) != want || len(app.library.Repositories) != want ||
			app.dialog != nil || app.problem != "" {
			t.Fatalf("移除确认结果错误: %+v, %s", app.library, app.problem)
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			t.Fatalf("移除记录影响了磁盘仓库: %v", err)
		}
	}
}

func TestNativeRepositoryEntryMenuMoveFailure(t *testing.T) {
	app, _, finish := nativeFixture(t)
	library, err := app.service.CreateGroup("日常开发")
	if err != nil {
		t.Fatal(err)
	}
	app.library, app.screen = library, "library"
	tester := ui.NewTester(app.view, 960, 640)
	if err := tester.Click("管理 " + app.library.Repositories[0].Name); err != nil {
		t.Fatal(err)
	}
	if _, err := app.service.RemoveGroup(library.Groups[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("移动到分组", "日常开发"); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if app.problem == "" || app.busy || app.library.Repositories[0].GroupID != "" ||
		app.service.Library().Repositories[0].GroupID != "" {
		t.Fatal("移动失败未显示错误或改变了原分组")
	}
}
