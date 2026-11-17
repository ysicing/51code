// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestNativeLFSMissingInstallationHelp(t *testing.T) {
	app, _, finish := nativeIdentityFixture(t)
	app.prefs.LFS = gitrepo.ExecutableOptions{Source: "custom", Path: filepath.Join(t.TempDir(), "missing-lfs")}
	tester := sidebarSettingsTester(t, app)
	if err := tester.Click("Git"); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	parent, editor, preferences := app.dialog, app.identityEditor, app.prefs.LFS
	editor.name = "Unsaved identity"
	clickNativeSettingsControl(t, tester, ui.RoleButton, locales.InstallLFS())
	if app.dialog == parent || app.dialog.parent != parent {
		t.Fatal("缺失 LFS 时没有打开安装指引")
	}
	var clipboard ui.Services
	help := ui.NewTester(func(c *ui.Context) {
		clipboard = c.Services()
		app.view(c)
	}, 780, 480)
	if _, command := lfsInstallCommand(runtime.GOOS); command != "" {
		if !help.HasText(command) {
			t.Fatal("安装指引缺少当前平台的命令")
		}
		if err := help.Click(locales.CopyInstallCommand()); err != nil {
			t.Fatal(err)
		}
		if clipboard.ReadClipboard() != command {
			t.Fatal("复制的安装命令不正确")
		}
	}
	help.FailOpenURL(errors.New("no browser"))
	if err := help.Click(locales.OpenLFSWebsite()); err != nil {
		t.Fatal(err)
	}
	if app.dialog.problem != locales.LfsWebsiteOpenFailed("no browser") {
		t.Fatal("官网打开失败时未保留安装指引或报告原因")
	}
	help.FailOpenURL(nil)
	if err := help.Click(locales.OpenLFSWebsite()); err != nil {
		t.Fatal(err)
	}
	urls := help.OpenedURLs()
	if len(urls) != 2 || urls[1] != "https://git-lfs.com/" || app.dialog.problem != "" {
		t.Fatal("官网入口无法重试或打开了错误地址")
	}
	help.Frame()
	if len(help.OpenedURLs()) != 2 {
		t.Fatal("重绘重复打开了官网")
	}
	for _, method := range []string{"button", "escape", "backdrop"} {
		if app.dialog == parent {
			app.lfsInstallDialog(parent)
			help.Frame()
		}
		switch method {
		case "button":
			if err := help.Click(locales.Close()); err != nil {
				t.Fatal(err)
			}
		case "escape":
			help.Key(0, ui.KeyEscape)
		case "backdrop":
			help.ClickAt(2, 2)
		}
		if app.dialog != parent || editor.name != "Unsaved identity" || app.prefs.LFS != preferences || app.identityEditor != editor {
			t.Fatalf("%s 关闭安装指引丢失了设置草稿或工具来源", method)
		}
	}
	tester.Frame()
	editor.lfsError = ""
	editor.lfsInfo = gitrepo.ExecutableInfo{Version: "git-lfs/test"}
	tester.Frame()
	if _, found := tester.AccessByRole(ui.RoleButton, locales.InstallLFS()); found {
		t.Fatal("工具可用时仍显示缺失安装入口")
	}
}

func TestNativeLFSInstallHelpLayout(t *testing.T) {
	app, _, _ := nativeFixture(t)
	for _, language := range []string{"zh-Hans", "zh-Hant", "en"} {
		t.Setenv("MYGO_LOCALE", language)
		app.lfsInstallDialog(nil)
		tester := ui.NewTester(app.view, 780, 480)
		tester.SetLocale(language)
		for _, dark := range []bool{false, true} {
			tester.SetDark(dark)
			tester.Frame()
			for _, label := range []string{locales.InstallLFSTitle(), locales.OpenLFSWebsite(), locales.LfsInstallHint(), locales.Close()} {
				bounds, found := tester.Find(label)
				if !found || bounds.W <= 0 || bounds.H <= 0 || bounds.X < 0 || bounds.Y < 0 || bounds.X+bounds.W > 780 || bounds.Y+bounds.H > 480 {
					t.Fatalf("安装指引被裁切：%s %+v", label, bounds)
				}
			}
			if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
				saveNativeScreenshot(t, tester, directory, fmt.Sprintf("lfs-install-%s-dark-%t.png", language, dark))
			}
		}
	}
}
