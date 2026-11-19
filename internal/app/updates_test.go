// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"errors"
	"os"
	"testing"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// stubSelfUpdates 固定“能否自更新”，测试结束后恢复。
func stubSelfUpdates(t *testing.T, enabled bool) {
	t.Helper()
	previous := selfUpdates
	selfUpdates = func() bool { return enabled }
	t.Cleanup(func() { selfUpdates = previous })
}

func TestNativeVersionButtonOpensReleasesWithoutSelfUpdate(t *testing.T) {
	stubSelfUpdates(t, false)
	app := &nativeApp{}
	tester := ui.NewTester(func(c *ui.Context) {
		if app.versionButton(c) {
			app.checkUpdates(c)
		}
	}, 280, 34)
	tester.FailOpenURL(errors.New("no browser"))
	if err := tester.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	if app.problem != "no browser" {
		t.Fatalf("打开下载页失败应在状态栏显示原因: %q", app.problem)
	}
	tester.FailOpenURL(nil)
	app.problem = ""
	if err := tester.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	// deb/rpm 等不能自更新的安装不应弹出 mygo 的“更新不可用”窗口，而是打开下载页。
	if urls := tester.OpenedURLs(); len(urls) != 2 || urls[1] != desktopReleasesURL || app.problem != "" {
		t.Fatalf("不能自更新时应打开 GitHub Releases: %v, %q", urls, app.problem)
	}
}

func TestNativeVersionUpdateEntry(t *testing.T) {
	app := &nativeApp{}
	clicks := 0
	tester := ui.NewTester(func(c *ui.Context) {
		if app.versionButton(c) {
			clicks++
		}
	}, 280, 34)
	if err := tester.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	if clicks != 1 {
		t.Fatal("点击当前版本未触发更新入口")
	}
	app.availableVersion = "0.0.6"
	tester.Frame()
	if _, found := tester.Find("新版本 v0.0.6"); !found {
		t.Fatal("发现更新后未显示可用版本")
	}
	if err := tester.Click("新版本 v0.0.6，检查更新"); err != nil {
		t.Fatal(err)
	}
	if clicks != 2 {
		t.Fatal("新版本提示无法再次打开更新入口")
	}
	app.availableVersion = ""
	tester.Frame()
	if _, found := tester.Find("新版本 v0.0.6"); found {
		t.Fatal("清除可用版本后仍显示过期提示")
	}
	if info, found := tester.AccessByRole(ui.RoleButton, "检查更新"); !found || info.Disabled {
		t.Fatal("清除更新提示后应仍可手动检查")
	}
}

func TestNativeVersionLabelUsesDevelopmentMode(t *testing.T) {
	previous := mygo.App.Version()
	mygo.App.SetVersion("0.3.2")
	t.Cleanup(func() { mygo.App.SetVersion(previous) })

	t.Setenv("MYGO_ENV", "")
	if label := buildVersionLabel(); label != locales.DevelopmentBuild() {
		t.Fatalf("开发包版本显示为 %q", label)
	}
	t.Setenv("MYGO_ENV", "production")
	if label := buildVersionLabel(); label != "v0.3.2" {
		t.Fatalf("正式包版本显示为 %q", label)
	}
}

func TestNativeUpdateNoticeLayout(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.availableVersion = "0.0.6"
	app.problem = "Git 操作失败，请查看详情"
	for _, dark := range []bool{false, true} {
		tester := ui.NewTester(app.view, 1024, 680)
		tester.SetDark(dark)
		tester.Frame()
		info, found := tester.AccessByRole(ui.RoleButton, "新版本 v0.0.6，检查更新")
		if !found || info.Disabled {
			t.Fatal("错误提示不应遮挡或禁用更新入口")
		}
		if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
			name := "update-notice-light.png"
			if dark {
				name = "update-notice-dark.png"
			}
			saveNativeScreenshot(t, tester, directory, name)
		}
	}
}
