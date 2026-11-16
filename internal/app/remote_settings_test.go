// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"

	"github.com/egoist/mygo/ui"
)

func TestNativeRemoteSettingsCompactList(t *testing.T) {
	app, _, finish := nativeIdentityFixture(t)
	address := "git@example.test:team/" + strings.Repeat("long-repository/", 10) + "repo.git"
	push := "ssh://push.example.test/team/repo.git"
	app.repository.remotes = []gitrepo.RemoteConfig{
		{Name: "flow", URLs: []string{"git@flow.example.test:team/repo.git"}, PushURLs: []string{"git@flow.example.test:team/repo.git"}},
		{Name: "origin-with-a-long-name", URLs: []string{address, "https://mirror.example.test/repo.git"}, PushURLs: []string{push}},
	}
	app.repositorySettingsDialog()
	finish()
	var clipboard ui.Services
	tester := ui.NewTester(func(c *ui.Context) {
		clipboard = c.Services()
		app.view(c)
	}, 1024, 680)
	if err := tester.Click("远程"); err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{1024, 680}, {780, 480}} {
		tester.SetSize(size[0], size[1])
		for _, dark := range []bool{false, true} {
			tester.SetDark(dark)
			tester.Frame()
			if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
				saveNativeScreenshot(t, tester, directory, fmt.Sprintf("remote-list-%d-dark-%t.png", size[0], dark))
			}
			panel, found := tester.AccessByRole(ui.RoleDialog, "")
			if !found {
				t.Fatal("缺少仓库设置")
			}
			var name ui.Rect
			for _, entry := range tester.AccessAll() {
				if entry.Name == "flow" && entry.Bounds.X > panel.Bounds.X {
					name = entry.Bounds
				}
			}
			if name.W == 0 {
				t.Fatal("缺少远程名称")
			}
			url, found := tester.Find("git@flow.example.test:team/repo.git")
			if !found || url.X <= name.X || math.Abs(float64(url.Y+url.H/2-name.Y-name.H/2)) > 2 {
				t.Fatalf("名称与地址没有同排对齐：name=%+v url=%+v", name, url)
			}
			for _, label := range []string{"管理 flow", "管理 origin-with-a-long-name", "添加远程"} {
				bounds, found := tester.Find(label)
				if !found || bounds.X < 0 || bounds.Y < 0 || bounds.X+bounds.W > float32(size[0]) || bounds.Y+bounds.H > float32(size[1]) {
					t.Fatalf("操作控件被裁切：%s", label)
				}
			}
		}
	}
	app.writable = false
	tester.Frame()
	if err := tester.Click("管理 origin-with-a-long-name"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("编辑…"); err == nil {
		t.Fatal("只读仓库允许编辑远程")
	}
	if err := tester.ChooseMenuItem("移除…"); err == nil {
		t.Fatal("只读仓库允许移除远程")
	}
	if err := tester.ChooseMenuItem("复制获取地址"); err != nil {
		t.Fatal(err)
	}
	if clipboard.ReadClipboard() != address+"\nhttps://mirror.example.test/repo.git" {
		t.Fatal("复制获取地址丢失了长地址或多个地址")
	}
	if err := tester.Click("管理 origin-with-a-long-name"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("复制推送地址"); err != nil {
		t.Fatal(err)
	}
	if clipboard.ReadClipboard() != push {
		t.Fatal("复制了错误的推送地址")
	}
	app.repository.remotes = nil
	app.writable = true
	tester.Frame()
	if _, found := tester.Find("尚未配置远程仓库"); !found {
		t.Fatal("缺少空状态")
	}
	if err := tester.Click("添加远程"); err != nil {
		t.Fatal(err)
	}
	if app.dialog == nil || len(app.dialog.fields) != 2 {
		t.Fatal("空状态无法添加远程")
	}
}
