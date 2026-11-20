// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestNativeWorktreesCleanupFilterPreservesSelection(t *testing.T) {
	state := &nativeWorktrees{selected: -1}
	items := []gitrepo.Worktree{
		{Path: "/main", Current: true},
		{Path: "/first", CleanupCandidate: true},
		{Path: "/second", CleanupCandidate: true},
	}
	state.setItems(items)
	state.selected = 2
	state.candidatesOnly = true
	state.filterItems()
	if len(state.items) != 2 || state.items[state.selected].Path != "/second" {
		t.Fatal("filter lost selection by path")
	}
	state.setItems(items[:2])
	if len(state.items) != 1 || state.selected != 0 || state.items[0].Path != "/first" {
		t.Fatal("refresh did not choose a remaining visible candidate")
	}
	state.setItems(items[:1])
	if len(state.items) != 0 || state.selected != -1 {
		t.Fatal("empty candidate filter retained a hidden selection")
	}
	state.candidatesOnly = false
	state.filterItems()
	if len(state.items) != 1 || state.selected != 0 || !state.items[0].Current {
		t.Fatal("returning to all worktrees lost the current worktree")
	}
}

func TestNativeWorktreesCleanupConfirmationAndRemoval(t *testing.T) {
	app, path, finish := nativeFixture(t)
	linked := filepath.Join(t.TempDir(), "candidate")
	nativeGit(t, path, "worktree", "add", "-b", "topic", linked)
	linked, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	writeNativeFile(t, path, ".git/info/exclude", "ignored.txt\n")
	writeNativeFile(t, linked, "ignored.txt", "ignored data")
	app.openWorktrees()
	finish()
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click(locales.WorktreeCleanupCandidates()); err != nil {
		t.Fatal(err)
	}
	if !app.worktrees.candidatesOnly || len(app.worktrees.items) != 1 || app.worktrees.items[0].Path != linked {
		t.Fatal("candidate filter did not hide main worktree")
	}
	if err := tester.Click(locales.CleanupWorktreeMenu()); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	dialog := app.dialog
	if dialog == nil || !dialog.danger || dialog.blocked || !strings.Contains(dialog.detail, linked) ||
		dialog.body == nil ||
		!strings.Contains(dialog.detail, locales.WorktreeRemoveDetail()) {
		t.Fatal("cleanup confirmation omitted ancestry, directory or ignored-file/branch retention warning")
	}
	if _, found := tester.Find(locales.WorktreeCleanupDetail("main")); !found {
		t.Fatal("cleanup confirmation did not render containing branches")
	}
	if err := tester.Click(locales.Cancel()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(linked, "ignored.txt")); err != nil {
		t.Fatal("cancel changed source files:", err)
	}
	if err := tester.Click(locales.CleanupWorktreeMenu()); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if err := tester.Click(locales.Remove()); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if app.dialog != nil || app.problem != "" || len(app.worktrees.items) != 0 || len(app.worktrees.allItems) != 1 {
		t.Fatal("cleanup did not refresh the empty candidate view", app.problem)
	}
	if _, found := tester.Find(locales.EmptyWorktreeCleanupCandidates()); !found {
		t.Fatal("empty candidate view omitted its empty state")
	}
	if nativeGit(t, path, "branch", "--list", "topic") == "" {
		t.Fatal("cleanup deleted retained branch")
	}
	if _, err := os.Stat(linked); !os.IsNotExist(err) {
		t.Fatalf("candidate directory not removed: %v", err)
	}
	if err := tester.Click(locales.AllWorktrees()); err != nil {
		t.Fatal(err)
	}
	if len(app.worktrees.items) != 1 || !app.worktrees.items[0].Current {
		t.Fatal("all worktrees unavailable after cleanup")
	}
}

func TestNativeWorktreesCleanupRechecksBeforeConfirmation(t *testing.T) {
	app, path, finish := nativeFixture(t)
	linked := filepath.Join(t.TempDir(), "candidate")
	nativeGit(t, path, "worktree", "add", "-b", "topic", linked)
	nativeGit(t, linked, "commit", "--allow-empty", "-m", "topic")
	nativeGit(t, path, "branch", "landed", "topic")
	app.openWorktrees()
	finish()
	if !app.worktrees.items[1].CleanupCandidate {
		t.Fatal("expected a candidate before target removal")
	}
	nativeGit(t, path, "branch", "-D", "landed")
	app.reviewWorktreeCleanup(linked)
	finish()
	if app.dialog == nil || !app.dialog.blocked || app.dialog.submit != nil || app.dialog.action != "" {
		t.Fatal("stale candidate offered removal after confirmation recheck")
	}
	app.dialog = nil
	app.reviewWorktree(linked, true)
	finish()
	if app.dialog == nil || app.dialog.blocked || app.dialog.submit == nil {
		t.Fatal("ordinary removal was coupled to candidate ancestry")
	}
}

func TestNativeWorktreesCleanupFailureRetainsConfirmation(t *testing.T) {
	app, path, finish := nativeFixture(t)
	linked := filepath.Join(t.TempDir(), "candidate")
	nativeGit(t, path, "worktree", "add", "-b", "topic", linked)
	nativeGit(t, linked, "commit", "--allow-empty", "-m", "topic")
	nativeGit(t, path, "branch", "landed", "topic")
	app.openWorktrees()
	finish()
	app.reviewWorktreeCleanup(linked)
	finish()
	dialog := app.dialog
	if dialog == nil || dialog.submit == nil {
		t.Fatal("missing cleanup confirmation")
	}
	nativeGit(t, path, "branch", "-D", "landed")
	dialog.submit(dialog)
	finish()
	dismissNativeError(t, app)
	if app.dialog != dialog || dialog.problem == "" {
		t.Fatal("failed cleanup lost confirmation and failure reason")
	}
	if _, err := os.Stat(filepath.Join(linked, "hello.txt")); err != nil {
		t.Fatal("stale cleanup removed source files:", err)
	}
}

func TestNativeWorktreesDiscoverAndOpen(t *testing.T) {
	app, path, finish := nativeFixture(t)
	linked := filepath.Join(t.TempDir(), "中文工作树")
	nativeGit(t, path, "worktree", "add", "-b", "topic", linked)
	linked, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click("工作树"); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if len(app.worktrees.items) != 2 {
		t.Fatal("没有发现同仓库的其他工作树")
	}
	if err := tester.Click(linked); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("打开工作树"); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if app.repository.snapshot.Path != linked || app.repository.snapshot.Branch != "topic" || app.screen != "changes" {
		t.Fatal("没有作为独立工作目录打开链接工作树")
	}
}

func TestNativeLinkedBranchDeletionConfirmation(t *testing.T) {
	app, path, finish := nativeFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	nativeGit(t, path, "worktree", "add", "-b", "topic", linked)
	linked, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	tester := ui.NewTester(app.view, 1024, 680)
	for _, cancel := range []bool{true, false} {
		app.deleteBranch("topic", false)
		if app.readTask == nil {
			t.Fatal("删除分支没有先预检关联工作树")
		}
		finish()
		tester.Frame()
		dialog := app.dialog
		if dialog == nil || dialog.action != "移除工作树并删除分支" ||
			!strings.Contains(dialog.detail, linked) || !dialog.danger {
			t.Fatal("没有明确确认关联工作树目录及删除分支")
		}
		if cancel {
			if err := tester.Click("取消"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(linked); err != nil || nativeGit(t, path, "branch", "--list", "topic") == "" {
				t.Fatal("取消确认改变了工作树或分支", err)
			}
			continue
		}
		if err := tester.Click("移除工作树并删除分支"); err != nil {
			t.Fatal(err)
		}
		finish()
		tester.Frame()
		if app.dialog != nil || app.problem != "" || nativeGit(t, path, "branch", "--list", "topic") != "" {
			t.Fatal("联动删除没有完成", app.problem)
		}
		if _, err := os.Stat(linked); !os.IsNotExist(err) {
			t.Fatalf("工作树目录未移除: %v", err)
		}
	}
}

func TestNativeDetachedWorktreeRemovalExplainsCommitRetention(t *testing.T) {
	app, path, finish := nativeFixture(t)
	linked := filepath.Join(t.TempDir(), "detached")
	nativeGit(t, path, "worktree", "add", "--detach", linked)
	nativeGit(t, linked, "commit", "--allow-empty", "-m", "detached commit")
	head := nativeGit(t, linked, "rev-parse", "HEAD")
	app.openWorktrees()
	finish()
	app.reviewWorktree(linked, true)
	finish()
	dialog := app.dialog
	if dialog == nil || dialog.blocked || !strings.Contains(dialog.detail, "分离 HEAD") ||
		!strings.Contains(dialog.detail, head) || strings.Contains(dialog.detail, "保留本地分支") {
		t.Fatal("分离 HEAD 的移除确认错误保证了分支会保留提交")
	}
}

func TestNativeWorktreeCreateAndFailurePreservesDraft(t *testing.T) {
	app, path, finish := nativeFixture(t)
	app.openWorktrees()
	finish()
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click("新建工作树…"); err != nil {
		t.Fatal(err)
	}
	dialog := app.dialog
	linked := filepath.Join(t.TempDir(), "linked")
	dialog.fields[0].value, dialog.fields[1].value, dialog.fields[2].value = linked, "topic", "HEAD"
	tester.Frame()
	if err := tester.Click("创建"); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if app.dialog != nil || len(app.worktrees.items) != 2 ||
		nativeGit(t, linked, "branch", "--show-current") != "topic" {
		t.Fatal("创建工作树失败或未刷新列表")
	}
	app.createWorktreeDialog()
	dialog = app.dialog
	dialog.fields[0].value, dialog.fields[1].value, dialog.fields[2].value = linked, "another", "HEAD"
	dialog.submit(dialog)
	finish()
	dismissNativeError(t, app)
	if app.dialog != dialog || dialog.problem == "" || dialog.fields[1].value != "another" {
		t.Fatal("创建失败未保留草稿和错误")
	}
	if nativeGit(t, path, "branch", "--list", "another") != "" {
		t.Fatal("失败创建留下无关分支")
	}
}

func TestNativeWorktreeMergeAndRemove(t *testing.T) {
	app, path, finish := nativeFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	nativeGit(t, path, "worktree", "add", "-b", "topic", linked)
	writeNativeFile(t, linked, "feature.txt", "feature\n")
	nativeGit(t, linked, "add", ".")
	nativeGit(t, linked, "commit", "-m", "feature")
	app.openWorktrees()
	finish()
	canonical, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click(canonical); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("工作树操作"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("合并到当前工作树…"); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	dialog := app.dialog
	if dialog == nil || !strings.Contains(dialog.detail, "topic") {
		t.Fatal("合并确认缺少源分支信息")
	}
	if err := tester.Click("合并"); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if app.problem != "" || app.dialog != nil || nativeGit(t, path, "rev-parse", "HEAD") != nativeGit(t, linked, "rev-parse", "HEAD") {
		t.Fatal("合并未更新当前工作树", app.problem)
	}
	if err := tester.Click("工作树操作"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("移除工作树…"); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	dialog = app.dialog
	if dialog == nil || !dialog.danger || !strings.Contains(dialog.detail, canonical) {
		t.Fatal("移除未明确确认目录")
	}
	if err := tester.Click("移除"); err != nil {
		t.Fatal(err)
	}
	finish()
	if app.problem != "" || len(app.worktrees.items) != 1 {
		t.Fatal("移除后未刷新工作树", app.problem)
	}
	if _, err := os.Stat(linked); !os.IsNotExist(err) {
		t.Fatalf("链接工作目录未移除: %v", err)
	}
	if nativeGit(t, path, "branch", "--list", "topic") == "" {
		t.Fatal("移除工作树误删本地分支")
	}
}

func TestNativeWorktreeSafetyAndCanceledReads(t *testing.T) {
	app, path, finish := nativeFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	nativeGit(t, path, "worktree", "add", "-b", "topic", linked)
	writeNativeFile(t, linked, "untracked.txt", "keep me\n")
	app.openWorktrees()
	finish()
	app.reviewWorktree(linked, true)
	finish()
	if app.dialog == nil || app.dialog.valid() && app.dialog.action != "" {
		t.Fatal("未提交内容没有阻止移除")
	}
	if _, err := os.Stat(filepath.Join(linked, "untracked.txt")); err != nil {
		t.Fatal("预检破坏工作树数据")
	}
	app.dialog = nil
	app.loadWorktrees()
	app.switchWorkspace("changes")
	finish()
	if app.screen != "changes" || app.readTask != nil {
		t.Fatal("离开工作树管理后应用了过期读取")
	}
}

func TestNativeWorktreeRemovalReviewsFiles(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		t.Run(fmt.Sprintf("ignored=%t", ignored), func(t *testing.T) {
			app, path, finish := nativeFixture(t)
			linked := filepath.Join(t.TempDir(), "linked")
			nativeGit(t, path, "worktree", "add", "-b", "topic", linked)
			if ignored {
				writeNativeFile(t, path, ".git/info/exclude", "local-data.txt\n")
			}
			writeNativeFile(t, linked, "local-data.txt", "keep me\n")
			app.openWorktrees()
			finish()
			canonical, err := filepath.EvalSymlinks(linked)
			if err != nil {
				t.Fatal(err)
			}
			tester := ui.NewTester(app.view, 1024, 680)
			if err := tester.Click(canonical); err != nil {
				t.Fatal(err)
			}
			if err := tester.Click("工作树操作"); err != nil {
				t.Fatal(err)
			}
			if err := tester.ChooseMenuItem("移除工作树…"); err != nil {
				t.Fatalf("应允许查看移除受阻原因: %v", err)
			}
			finish()
			tester.Frame()
			dialog := app.dialog
			if ignored {
				if dialog == nil || dialog.blocked || dialog.action != "移除" || dialog.submit == nil {
					t.Fatal("忽略文件不应阻止移除确认")
				}
				if !strings.Contains(dialog.detail, "忽略文件") {
					t.Fatal("确认时没有说明忽略文件也会删除")
				}
			} else if dialog == nil || !dialog.blocked || dialog.action != "" || dialog.submit != nil {
				t.Fatal("未跟踪文件应阻止执行移除")
			} else if !strings.Contains(dialog.problem, "未提交或未跟踪文件") {
				t.Fatalf("没有解释文件保护原因: %s", dialog.problem)
			}
			data, err := os.ReadFile(filepath.Join(linked, "local-data.txt"))
			if err != nil || string(data) != "keep me\n" {
				t.Fatalf("预检改变了用户文件: %q %v", data, err)
			}
			if ignored {
				if err := tester.Click("移除"); err != nil {
					t.Fatal(err)
				}
				finish()
				if app.problem != "" || app.dialog != nil || len(app.worktrees.items) != 1 {
					t.Fatal("未能移除含忽略文件的工作树", app.problem)
				}
				if _, err := os.Stat(linked); !os.IsNotExist(err) {
					t.Fatalf("确认后工作目录未移除: %v", err)
				}
				if nativeGit(t, path, "branch", "--list", "topic") == "" {
					t.Fatal("移除工作树误删分支")
				}
			}
		})
	}
}

func TestNativeWorktreesAppearanceAndReadOnly(t *testing.T) {
	app, path, finish := nativeFixture(t)
	linked := filepath.Join(t.TempDir(), strings.Repeat("long-worktree-", 8))
	nativeGit(t, path, "worktree", "add", "-b", "feature/"+strings.Repeat("long-name-", 8), linked)
	linked, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	app.openWorktrees()
	finish()
	app.writable = false
	for _, size := range [][2]int{{1024, 680}, {1320, 860}} {
		for _, appearance := range []string{appearanceLight, appearanceDark} {
			app.prefs.Appearance = appearance
			tester := ui.NewTester(app.view, size[0], size[1])
			if create, found := tester.AccessByRole(ui.RoleButton, "新建工作树…"); !found || !create.Disabled {
				t.Fatal("仓库不可写时仍允许创建工作树")
			}
			if err := tester.Click(linked); err != nil {
				t.Fatal(err)
			}
			if open, found := tester.AccessByRole(ui.RoleButton, "打开工作树"); !found || open.Disabled || open.Offscreen {
				t.Fatal("不可写仓库仍应允许打开工作树")
			}
			if cleanup, found := tester.AccessByRole(ui.RoleButton, locales.CleanupWorktreeMenu()); !found ||
				!cleanup.Disabled || cleanup.Offscreen {
				t.Fatal("不可写仓库的清理按钮应可见但禁用")
			}
			if filter, found := tester.AccessByRole(ui.RoleRadio, locales.WorktreeCleanupCandidates()); !found ||
				filter.Disabled || filter.Offscreen {
				t.Fatal("不可写仓库仍应允许筛选清理候选")
			}
			if _, found := tester.Find(locales.WorktreeContainedIn("main")); !found {
				t.Fatal("没有展示通过祖先关系确认包含 HEAD 的分支")
			}
			if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
				saveNativeScreenshot(t, tester, directory, fmt.Sprintf("worktrees-%s-%d.png", appearance, size[0]))
			}
		}
	}
}
