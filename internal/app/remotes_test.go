// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestNativeRemoteBranchTree(t *testing.T) {
	app, path, finish := nativeFixture(t)
	nativeGit(t, path, "update-ref", "refs/remotes/origin/main", "HEAD")
	app.repository.remotes = []gitrepo.RemoteConfig{{Name: "origin"}, {Name: "upstream"}, {Name: "team/mirror"}, {Name: "empty"}}
	app.repository.branches = append(app.repository.branches,
		gitrepo.Branch{Name: "origin/HEAD", Remote: true},
		gitrepo.Branch{Name: "origin/main", Remote: true},
		gitrepo.Branch{Name: "origin/feature/work", Remote: true},
		gitrepo.Branch{Name: "upstream/main", Remote: true},
		gitrepo.Branch{Name: "team/mirror/release/next", Remote: true},
	)
	tester := ui.NewTester(app.view, 1320, 860)
	for _, label := range []string{"远端 origin", "远端 upstream", "远端 team/mirror", "远端 empty"} {
		if _, found := tester.Find(label); !found {
			t.Fatalf("缺少远端分组 %s", label)
		}
	}
	if _, found := tester.Find("origin/main"); found {
		t.Fatal("远端默认展开导致分支列表占满侧栏")
	}
	for _, label := range []string{"远端 origin", "远端 upstream", "远端 team/mirror"} {
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
		tester.Frame()
	}
	parent, _ := tester.Find("远端 origin")
	child, found := tester.Find("origin/main")
	if !found || child.Y <= parent.Y {
		t.Fatal("远程分支未显示在所属远端下")
	}
	if err := tester.Click("远端 origin"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if _, found := tester.Find("origin/main"); found {
		t.Fatal("折叠后仍显示远端的分支")
	}
	if _, found := tester.Find("upstream/main"); !found {
		t.Fatal("折叠一个远端影响其他远端")
	}
	refreshed := *app.repository
	app.repository = &refreshed
	tester.Frame()
	if _, found := tester.Find("origin/main"); found {
		t.Fatal("刷新仓库后丢失折叠状态")
	}
	tester.Key(0, ui.KeyRight)
	tester.Frame()
	if _, found := tester.Find("origin/main"); !found {
		t.Fatal("右方向键未展开远端")
	}
	if err := tester.Click("origin/HEAD"); err != nil {
		t.Fatal(err)
	}
	tester.Key(0, ui.KeyDown)
	tester.Key(0, ui.KeyEnter)
	finish()
	if app.problem != "" || app.dialog != nil || app.viewedRef.kind != "远端" || app.viewedRef.reference != "origin/main" ||
		app.historyRequest() != (nativeHistoryRequest{}) ||
		app.commitHash != app.repository.snapshot.Head || app.repository.snapshot.Branch != "main" {
		t.Fatalf("键盘导航未定位所属远端分支的提交: %s", app.problem)
	}
	app.dialog = nil
	tester.Frame()
	if err := tester.Click("origin/HEAD"); err != nil {
		t.Fatal(err)
	}
	if app.dialog != nil {
		t.Fatal("HEAD 默认引用不应打开检出弹窗")
	}
	for _, branch := range []struct{ reference, name string }{
		{"origin/feature/work", "feature/work"},
		{"upstream/main", "main"},
		{"team/mirror/release/next", "release/next"},
	} {
		if err := tester.RightClick(branch.reference); err != nil {
			t.Fatal(err)
		}
		if err := tester.ChooseMenuItem(locales.CopyBranchName()); err != nil || tester.Clipboard() != branch.reference {
			t.Fatalf("无法复制远程分支的全名 %s: %v", branch.reference, err)
		}
		if err := tester.RightClick(branch.reference); err != nil {
			t.Fatal(err)
		}
		if err := tester.ChooseMenuItem(locales.CheckoutRemoteBranch()); err != nil {
			t.Fatal(err)
		}
		if app.dialog == nil || app.dialog.detail != branch.reference || app.dialog.fields[0].value != branch.name {
			t.Fatalf("检出的远程引用或本地分支名错误: %+v", branch)
		}
		app.dialog = nil
		tester.Frame()
	}
	if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
		saveNativeScreenshot(t, tester, directory, "native-remote-tree-light.png")
		tester.SetDark(true)
		saveNativeScreenshot(t, tester, directory, "native-remote-tree-dark.png")
	}
}
