// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestNativeSubmoduleBackgroundRefreshKeepsLastSuccessfulList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git fault injection uses a POSIX executable wrapper")
	}
	app, parent, finish := nativeFixture(t)
	const path = "deps/module"
	nativeGit(t, parent, "update-index", "--add", "--cacheinfo", "160000,"+app.repository.snapshot.Head+","+path)
	app.switchWorkspace("submodules")
	app.loadSubmodules()
	finish()
	tester := ui.NewTester(app.view, 1024, 680)
	if !tester.HasText(path) {
		t.Fatal("初次读取未显示子模块")
	}

	actual, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XSOURCE_TEST_REAL_GIT", actual)
	const failure = "submodule index read failed"
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nfor arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = ls-files ]; then printf '%s\\n' '" + failure + "' >&2; exit 1; fi\n" +
		"done\nexec \"$XSOURCE_TEST_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gitrepo.ConfigureExecutable(gitrepo.ExecutableOptions{Source: "custom", Path: wrapper})
	t.Cleanup(func() { gitrepo.ConfigureExecutable(gitrepo.ExecutableOptions{Source: "system"}) })

	nativeGit(t, parent, "update-index", "--force-remove", "--", path)
	app.refreshRepositoryView(true)
	finish()
	tester.Frame()
	if !tester.HasText(path) || !app.writable {
		t.Fatal("子模块读取失败清空了旧列表或阻止了仓库写操作")
	}
	notice := locales.SubmoduleRefreshFailed()
	if !tester.HasText(notice) {
		t.Fatal("后台刷新失败未提示")
	}
	if err := tester.Click(locales.ShowDetails()); err != nil {
		t.Fatal(err)
	}
	if app.dialog == nil || app.dialog.errorDetails == nil || !tester.HasText(notice+"\n"+failure) {
		t.Fatal("无法查看子模块刷新失败的 Git 错误")
	}
	if err := tester.Click(locales.Close()); err != nil {
		t.Fatal(err)
	}

	gitrepo.ConfigureExecutable(gitrepo.ExecutableOptions{Source: "system"})
	app.refreshOnFocus()
	finish()
	tester.Frame()
	if tester.HasText(path) || tester.HasText(notice) || !tester.HasText(locales.NoSubmodules()) {
		t.Fatal("后台刷新恢复后未更新列表或清除失败提示")
	}
}

func TestNativeSubmoduleAddInitializeAndOpen(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	app, parent, finish := nativeFixture(t)
	_, source, _ := nativeFixture(t)
	commit := nativeGit(t, source, "rev-parse", "HEAD")
	const path = "deps/示例 组件"
	tester := ui.NewTester(app.view, 1024, 680)
	click := func(label string) {
		t.Helper()
		if err := tester.Click(label); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	press := func(label string) {
		t.Helper()
		button, found := tester.AccessByRole(ui.RoleButton, label)
		if !found || button.Disabled || button.Offscreen {
			t.Fatalf("按钮 %q 不可用: %+v", label, button)
		}
		tester.ClickAt(button.Bounds.X+button.Bounds.W/2, button.Bounds.Y+button.Bounds.H/2)
	}
	choose := func(label string) {
		t.Helper()
		if err := tester.ChooseMenuItem(label); err != nil {
			t.Fatal(err)
		}
	}
	complete := func() {
		t.Helper()
		finish()
		tester.Frame()
		if app.problem != "" {
			t.Fatal(app.problem)
		}
	}
	click(locales.Submodules())
	complete()
	if app.screen != "submodules" || !tester.HasText(locales.NoSubmodules()) {
		t.Fatal("侧栏没有打开子模块空页面")
	}
	click(locales.AddSubmodule())
	for _, field := range []struct{ label, value string }{
		{locales.RemoteAddress(), source}, {locales.Path(), path},
	} {
		input, present := tester.AccessByRole(ui.RoleTextField, field.label)
		if !present {
			t.Fatalf("添加表单缺少输入框 %q", field.label)
		}
		tester.ClickAt(input.Bounds.X+input.Bounds.W/2, input.Bounds.Y+input.Bounds.H/2)
		tester.Type(field.value)
	}
	tester.Frame()
	press(locales.Add())
	if !app.busy {
		t.Fatalf("添加表单没有启动 Git 操作: %+v", app.dialog.fields)
	}
	complete()
	if app.dialog != nil || !tester.HasText(path) || !tester.HasText(locales.SubmoduleUpToDate()) {
		t.Fatal("添加后没有更新子模块列表")
	}
	nativeGit(t, parent, "commit", "-m", "register the module")
	nativeGit(t, parent, "submodule", "deinit", "--", path)
	app.refresh()
	complete()
	if !tester.HasText(locales.SubmoduleUninitialized()) {
		t.Fatal("没有显示未初始化状态")
	}
	if err := tester.RightClick(path); err != nil {
		t.Fatal(err)
	}
	choose(locales.InitializeSubmodule())
	if app.dialog == nil || app.dialog.subject != path || app.dialog.detail != locales.UpdateSubmoduleDetail(commit) {
		t.Fatal("初始化没有确认路径和目标提交")
	}
	press(locales.InitializeSubmodule())
	if !app.busy {
		t.Fatal("初始化确认没有启动 Git 操作")
	}
	complete()
	if !tester.HasText(locales.SubmoduleUpToDate()) || nativeGit(t, filepath.Join(parent, path), "rev-parse", "HEAD") != commit {
		t.Fatal("初始化没有检出记录的提交")
	}
	for _, dark := range []bool{false, true} {
		tester.SetDark(dark)
		if _, found := tester.Find(locales.SubmoduleActions()); !found {
			t.Fatal("紧凑窗口缺少子模块菜单")
		}
		if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
			name := "submodules-light.png"
			if dark {
				name = "submodules-dark.png"
			}
			saveNativeScreenshot(t, tester, directory, name)
		}
	}
	click(locales.SubmoduleActions())
	choose(locales.Open())
	complete()
	complete()
	expected, err := filepath.EvalSymlinks(filepath.Join(parent, path))
	if err != nil || app.repository.snapshot.Path != expected || app.screen != "changes" {
		t.Fatalf("没有打开独立的子模块工作区: %q, %v", app.repository.snapshot.Path, err)
	}
}
