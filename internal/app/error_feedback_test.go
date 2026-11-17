// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

const worktreeErrorExample = "error: cannot delete branch 'feature/a' used by worktree at '/tmp/other-worktree'"

func dismissNativeError(t *testing.T, app *nativeApp) {
	t.Helper()
	if app.dialog == nil || app.dialog.errorDetails == nil {
		t.Fatal("预期直接显示错误弹窗")
	}
	app.closeDialog(app.dialog)
}

func TestNativeCanceledTaskDoesNotOpenError(t *testing.T) {
	app, _, finish := nativeFixture(t)
	app.runTask("取消操作", true, func(context.Context) (func(), error) {
		return nil, context.Canceled
	})
	finish()
	if app.dialog != nil || app.busy {
		t.Fatal("主动取消不应弹出错误或保持忙碌")
	}
}

func TestNativeFailedBranchDeletionOpensErrorImmediately(t *testing.T) {
	app, path, finish := nativeFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	nativeGit(t, path, "worktree", "add", "-b", "feature/a", linked)
	tester := ui.NewTester(app.view, 1024, 680)
	for range 2 {
		app.mutate("删除分支", func(ctx context.Context, path string) error {
			return app.service.DeleteBranch(ctx, path, "feature/a", false)
		})
		finish()
		tester.Frame()
		if app.dialog == nil || app.dialog.errorDetails == nil || app.dialog.title != "无法删除分支" {
			t.Fatal("操作失败后没有直接显示错误弹窗")
		}
		if _, found := tester.AccessByRole(ui.RoleButton, "查看详情"); found {
			t.Fatal("错误弹窗打开时仍重复显示查看详情入口")
		}
		if err := tester.Click("关闭"); err != nil {
			t.Fatal(err)
		}
		tester.Frame()
		if app.dialog != nil || app.problem != "" {
			t.Fatal("关闭错误弹窗后仍残留重复提示")
		}
	}
	if nativeGit(t, linked, "branch", "--show-current") != "feature/a" {
		t.Fatal("失败删除改变了工作树分支")
	}
}

func TestNativeFailedFormOpensErrorAndPreservesDraft(t *testing.T) {
	app, path, finish := nativeFixture(t)
	nativeGit(t, path, "branch", "topic")
	app.renameBranchDialog("topic")
	parent := app.dialog
	parent.fields[0].value = "main"
	tester := ui.NewTester(app.view, 1024, 680)
	for _, closeAction := range []string{"关闭", "关闭错误详情", "escape", "outside"} {
		tester.Frame()
		if err := tester.Click("重命名"); err != nil {
			t.Fatal(err)
		}
		finish()
		tester.Frame()
		if app.dialog == parent || app.dialog == nil || app.dialog.errorDetails == nil || app.dialog.parent != parent {
			t.Fatal("表单失败后没有直接显示错误或丢失了父表单")
		}
		switch closeAction {
		case "escape":
			tester.Key(0, ui.KeyEscape)
		case "outside":
			tester.Press(2, 2)
			tester.Release(2, 2)
		default:
			if err := tester.Click(closeAction); err != nil {
				t.Fatal(err)
			}
		}
		tester.Frame()
		if app.dialog != parent || parent.fields[0].value != "main" || app.problem != "" {
			t.Fatalf("%s 关闭错误后没有返回原表单草稿", closeAction)
		}
	}
}

func TestNativeFailedReadOpensErrorImmediately(t *testing.T) {
	app, _, finish := nativeFixture(t)
	app.selectCommit(strings.Repeat("f", 40))
	finish()
	if app.dialog == nil || app.dialog.errorDetails == nil || app.dialog.errorDetails.raw != app.problem {
		t.Fatal("读取失败后没有直接显示完整错误")
	}
}

func TestDescribeNativeProblem(t *testing.T) {
	for _, test := range []struct {
		name    string
		raw     string
		title   string
		summary string
		hint    bool
	}{
		{"worktree", worktreeErrorExample, "无法删除分支", "feature/a", true},
		{"checked-out", "error: cannot delete branch 'feature/a' checked out at '/tmp/other-worktree'", "无法删除分支", "feature/a", true},
		{"unmerged", "error: the branch 'feature/a' is not fully merged", "无法删除分支", "尚未合并", true},
		{"conflict", "Auto-merging hello.txt\nCONFLICT (content): Merge conflict in hello.txt\nAutomatic merge failed", "存在合并冲突", "冲突", true},
		{"authentication", "fatal: Authentication failed for 'https://example.test/repo.git'", "身份验证失败", "身份验证", true},
		{"ssh", "git@example.test: Permission denied (publickey).", "身份验证失败", "身份验证", true},
		{"network", "fatal: unable to access 'https://example.test/repo.git': Could not resolve host: example.test", "无法连接远端", "远端服务器", true},
		{"permissions", "error: unable to create file test.txt: Permission denied", "权限不足", "权限", true},
		{"unknown-git", "fatal: an unexpected Git error\nsecond line", "操作失败", "操作未完成", false},
		{"validation", "请输入提交说明", "操作失败", "请输入提交说明", false},
		{"git-unavailable", "Git 不可用，请选择可用的 Git", "操作失败", "Git 不可用", false},
		{"unknown", "自定义失败原因\n完整诊断输出", "操作失败", "自定义失败原因", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			problem := describeNativeProblem(test.raw)
			if problem.title != test.title || !strings.Contains(problem.summary, test.summary) ||
				(problem.hint != "") != test.hint || problem.raw != test.raw {
				t.Fatalf("错误信息分类或原始诊断不正确: %+v", problem)
			}
		})
	}
}

func TestNativeErrorDetailsCopyAndClose(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.problem = worktreeErrorExample
	var clipboard ui.Services
	tester := ui.NewTester(func(c *ui.Context) {
		clipboard = c.Services()
		app.view(c)
	}, 1024, 680)
	if err := tester.Click("查看详情"); err != nil {
		t.Fatal(err)
	}
	if !tester.HasText("无法删除分支") {
		t.Fatal("错误详情没有展示具体操作")
	}
	if err := tester.Click("复制详情"); err != nil {
		t.Fatal(err)
	}
	if clipboard.ReadClipboard() != worktreeErrorExample {
		t.Fatal("复制详情未保留完整原始错误")
	}
	if err := tester.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	if app.dialog != nil || app.problem != "" {
		t.Fatal("关闭详情没有同时清除错误提示")
	}
}

func TestNativeErrorDetailsAvoidsRepeatedMessage(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.problem = "所选目录不是 Git 工作区"
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click("查看详情"); err != nil {
		t.Fatal(err)
	}
	panel, found := tester.AccessByRole(ui.RoleDialog, "")
	if !found {
		t.Fatal("缺少错误详情窗口")
	}
	copies := 0
	for _, entry := range tester.AccessAll() {
		if entry.Name == app.problem && entry.Bounds.Y >= panel.Bounds.Y &&
			entry.Bounds.Y+entry.Bounds.H <= panel.Bounds.Y+panel.Bounds.H {
			copies++
		}
	}
	if copies != 1 {
		t.Fatalf("同一错误在详情中重复显示了 %d 次", copies)
	}
}

func TestNativeInlineProblemPreservesParentDraft(t *testing.T) {
	app, _, _ := nativeFixture(t)
	parent := &nativeDialog{
		title: "编辑远端", action: "保存",
		fields:  []nativeField{{label: "地址", value: "https://example.test/unsaved.git"}},
		problem: worktreeErrorExample,
	}
	app.dialog = parent
	tester := ui.NewTester(app.view, 1024, 680)
	for _, closeAction := range []string{"关闭", "关闭错误详情", "escape", "outside"} {
		if err := tester.Click("查看详情"); err != nil {
			t.Fatal(err)
		}
		if app.dialog == parent || app.dialog.parent != parent {
			t.Fatal("查看详情没有保留原表单")
		}
		switch closeAction {
		case "escape":
			tester.Key(0, ui.KeyEscape)
		case "outside":
			tester.Press(2, 2)
			tester.Release(2, 2)
		default:
			if err := tester.Click(closeAction); err != nil {
				t.Fatal(err)
			}
		}
		tester.Frame()
		if app.dialog != parent || parent.fields[0].value != "https://example.test/unsaved.git" ||
			parent.problem != worktreeErrorExample {
			t.Fatalf("%s 关闭详情后丢失了原表单草稿", closeAction)
		}
	}
}

func TestNativeErrorDetailsSizingAndDismissal(t *testing.T) {
	app, _, _ := nativeFixture(t)
	for _, size := range [][2]int{{1771, 860}, {1024, 680}, {780, 480}} {
		for _, dark := range []bool{false, true} {
			for _, long := range []bool{false, true} {
				name := fmt.Sprintf("%d-dark-%t-long-%t", size[0], dark, long)
				t.Run(name, func(t *testing.T) {
					app.dialog = nil
					app.problem = worktreeErrorExample
					if long {
						app.problem += "\n" + strings.Repeat("remote: "+strings.Repeat("输出内容", 60)+"\n", 80)
					}
					app.prefs.Appearance = appearanceLight
					if dark {
						app.prefs.Appearance = appearanceDark
					}
					tester := ui.NewTester(app.view, size[0], size[1])
					if err := tester.Click("查看详情"); err != nil {
						t.Fatal(err)
					}
					panel, found := tester.AccessByRole(ui.RoleDialog, "")
					if !found || panel.Bounds.X < 0 || panel.Bounds.Y < 0 ||
						panel.Bounds.X+panel.Bounds.W > float32(size[0]) ||
						panel.Bounds.Y+panel.Bounds.H > float32(size[1]) {
						t.Fatalf("详情窗口超出可用区域: %+v", panel)
					}
					if !long && panel.Bounds.H > 340 {
						t.Fatalf("短错误详情保留了过多空白: %+v", panel.Bounds)
					}
					for _, label := range []string{"复制详情", "关闭错误详情", "关闭"} {
						control, found := tester.AccessByRole(ui.RoleButton, label)
						if !found || control.Offscreen || control.Bounds.X < panel.Bounds.X ||
							control.Bounds.Y+control.Bounds.H > panel.Bounds.Y+panel.Bounds.H {
							t.Fatalf("详情操作不可见: %s %+v", label, control)
						}
					}
					if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
						saveNativeScreenshot(t, tester, directory, "error-details-"+name+".png")
					}
					tester.Key(0, ui.KeyEscape)
					tester.Frame()
					if app.dialog != nil {
						t.Fatal("Esc 未关闭详情")
					}
				})
			}
		}
	}
}
