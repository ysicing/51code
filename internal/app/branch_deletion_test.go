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
	for _, scenario := range []string{"unmerged", "forced", "dirty-forced", "stale", "partial"} {
		t.Run(scenario, func(t *testing.T) {
			app, path, finish := nativeFixture(t)
			linked := filepath.Join(t.TempDir(), "linked")
			nativeGit(t, path, "worktree", "add", "-b", "topic", linked)
			force := scenario == "forced" || scenario == "dirty-forced"
			if scenario == "unmerged" || scenario == "forced" {
				nativeGit(t, linked, "commit", "--allow-empty", "-m", "unmerged")
			}
			if scenario == "dirty-forced" {
				writeNativeFile(t, linked, "hello.txt", "keep changes\n")
			}
			app.deleteBranch("topic", force)
			finish()
			tester := ui.NewTester(app.view, 1024, 680)
			dialog := app.dialog
			if dialog == nil {
				t.Fatal("没有显示预检结果")
			}
			if scenario == "unmerged" || scenario == "dirty-forced" {
				if !dialog.blocked || dialog.action != "" || dialog.submit != nil || dialog.problem == "" {
					t.Fatal("不安全的联动删除未被阻止")
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
