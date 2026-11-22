// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestNativePartialBranchDeletionCancellationShowsOutcome(t *testing.T) {
	app, _, finish := nativeFixture(t)
	app.runTask("删除分支", false, func(context.Context) (func(), error) {
		return nil, &gitrepo.BranchDeletionPartialError{
			Branch: "topic", Path: "/tmp/removed-worktree", Head: strings.Repeat("a", 40), Err: context.Canceled,
		}
	})
	finish()
	if app.dialog == nil || app.dialog.errorDetails == nil || !strings.Contains(app.problem, "工作树已移除") {
		t.Fatal("部分完成结果被当作普通取消而隐藏")
	}
}

func TestNativeLinkedBranchDeletionSafety(t *testing.T) {
	for _, scenario := range []string{"unmerged", "forced", "dirty", "dirty-forced", "stale", "partial"} {
		t.Run(scenario, func(t *testing.T) {
			app, path, finish := nativeFixture(t)
			linked := filepath.Join(t.TempDir(), "linked")
			nativeGit(t, path, "worktree", "add", "-b", "topic", linked)
			force := scenario == "forced" || scenario == "dirty-forced"
			if scenario == "unmerged" || scenario == "forced" || scenario == "dirty" {
				nativeGit(t, linked, "commit", "--allow-empty", "-m", "unmerged")
			}
			if scenario == "dirty" || scenario == "dirty-forced" {
				writeNativeFile(t, linked, "hello.txt", "keep changes\n")
			}
			app.deleteBranch("topic", force)
			finish()
			tester := ui.NewTester(app.view, 1024, 680)
			dialog := app.dialog
			if dialog == nil {
				t.Fatal("没有显示预检结果")
			}
			if scenario == "unmerged" || scenario == "dirty" || scenario == "dirty-forced" {
				if !dialog.blocked || dialog.action != "" || dialog.submit != nil || dialog.problem == "" {
					t.Fatal("不安全的联动删除未被阻止")
				}
				const forceAction = "强制删除分支…"
				if scenario == "unmerged" {
					writeNativeFile(t, linked, "hello.txt", "changed after review\n")
					if err := tester.Click(forceAction); err != nil {
						t.Fatal(err)
					}
					finish()
					tester.Frame()
					if app.dialog == nil || app.dialog == dialog || app.dialog.action != "" || !tester.Focused(locales.Close()) {
						t.Fatal("重新预检应拦截后来出现的工作树更改")
					}
				}
				if _, found := tester.AccessByRole(ui.RoleButton, forceAction); found {
					t.Fatal("含本地更改的工作树不应提供强制删除入口")
				}
				if _, err := os.Stat(linked); err != nil || nativeGit(t, path, "branch", "--list", "topic") == "" {
					t.Fatal("预检删除了工作树或分支", err)
				}
				return
			}
			if force && (!strings.Contains(dialog.title, "强制删除分支") || !strings.Contains(dialog.detail, "未合并")) {
				t.Fatal("强制删除没有明确提示提交引用风险")
			}
			if scenario == "stale" {
				writeNativeFile(t, linked, "precious.txt", "keep me\n")
			}
			if scenario == "partial" {
				writeNativeFile(t, path, ".git/refs/heads/topic.lock", "held\n")
			}
			if err := tester.Click("移除工作树并删除分支"); err != nil {
				t.Fatal(err)
			}
			finish()
			tester.Frame()
			if scenario == "forced" {
				if app.dialog != nil || app.problem != "" || nativeGit(t, path, "branch", "--list", "topic") != "" {
					t.Fatal("明确强制确认后未删除分支", app.problem)
				}
				if _, err := os.Stat(linked); !os.IsNotExist(err) {
					t.Fatal("强制确认后未移除工作树", err)
				}
				return
			}
			if app.dialog == nil || app.dialog.errorDetails == nil {
				t.Fatal("执行失败没有直接显示错误")
			}
			if nativeGit(t, path, "branch", "--list", "topic") == "" {
				t.Fatal("执行失败丢失了分支")
			}
			if scenario == "stale" {
				if data, err := os.ReadFile(filepath.Join(linked, "precious.txt")); err != nil || string(data) != "keep me\n" {
					t.Fatal("过期确认丢失了新文件", err)
				}
			} else {
				if !strings.Contains(app.problem, "工作树已移除") {
					t.Fatal("没有说明部分完成状态", app.problem)
				}
				dismissNativeError(t, app)
				if app.dialog != dialog || !dialog.blocked || dialog.action != "" || dialog.submit != nil {
					t.Fatal("部分完成后仍允许重复执行旧的联动确认")
				}
			}
		})
	}
}

func TestNativeForceDeleteUnmergedBranchFromBlockedDialog(t *testing.T) {
	app, path, finish := nativeFixture(t)
	const branch = "feature/notifications"
	nativeGit(t, path, "switch", "-c", branch)
	nativeGit(t, path, "commit", "--allow-empty", "-m", "unmerged")
	nativeGit(t, path, "switch", "main")
	head := nativeGit(t, path, "rev-parse", "refs/heads/"+branch)
	app.deleteBranch(branch, false)
	finish()
	tester := ui.NewTester(app.view, 1024, 680)
	if !tester.Focused(locales.Close()) {
		t.Fatal("被拦截后应默认聚焦关闭")
	}
	if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
		saveNativeScreenshot(t, tester, directory, "branch-delete-blocked-light.png")
		tester.SetDark(true)
		saveNativeScreenshot(t, tester, directory, "branch-delete-blocked-dark.png")
	}
	tester.Key(0, ui.KeyTab)
	if !tester.Focused("强制删除分支…") {
		t.Fatal("Tab 应从关闭移到强制删除入口")
	}
	tester.Key(0, ui.KeyEnter)
	finish()
	tester.Frame()
	if !tester.HasText(locales.ForceDeleteLocalBranch()) || !tester.HasText(branch) ||
		!tester.Focused(locales.Cancel()) || nativeGit(t, path, "rev-parse", "refs/heads/"+branch) != head {
		t.Fatal("强制删除入口应展示二次确认并保留分支")
	}
	if err := tester.Click(locales.Delete()); err != nil {
		t.Fatal(err)
	}
	finish()
	if app.dialog != nil || app.problem != "" || nativeGit(t, path, "branch", "--list", branch) != "" {
		t.Fatal("二次确认后应删除分支", app.problem)
	}
}

func TestNativeBranchDeletionReviewPreservesNewDialog(t *testing.T) {
	app, path, finish := nativeFixture(t)
	nativeGit(t, path, "branch", "topic")
	app.deleteBranch("topic", false)
	opened := &nativeDialog{title: "设置", fields: []nativeField{{value: "keep draft"}}}
	app.dialog = opened
	finish()
	if app.dialog != opened || opened.fields[0].value != "keep draft" {
		t.Fatal("预检结果覆盖了后来打开的弹窗草稿")
	}
}

func TestNativeLinkedBranchConfirmationLayout(t *testing.T) {
	app, path, finish := nativeFixture(t)
	nativeGit(t, path, "config", "--local", "core.longpaths", "true")
	name := "feature/" + strings.Repeat("long-branch-name/", 12) + "end"
	linked := filepath.Join(t.TempDir(), strings.Repeat("long-worktree-", 8))
	nativeGit(t, path, "worktree", "add", "-b", name, linked)
	for _, width := range []int{480, 1024} {
		for _, dark := range []bool{false, true} {
			app.deleteBranch(name, true)
			finish()
			tester := ui.NewTester(app.dialogView, width, 680)
			tester.SetDark(dark)
			for _, label := range []string{"取消", "移除工作树并删除分支"} {
				control, found := tester.AccessByRole(ui.RoleButton, label)
				if !found || control.Offscreen || control.Bounds.X < 0 ||
					control.Bounds.X+control.Bounds.W > float32(width) || control.Bounds.Y+control.Bounds.H > 680 {
					t.Fatalf("联动删除确认操作不可见: %s %+v", label, control)
				}
			}
			if !tester.Focused("取消") {
				t.Fatal("危险操作未默认聚焦取消")
			}
			app.closeDialog(app.dialog)
		}
	}
}
