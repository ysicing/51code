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

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestNativeGitFeatureDefaultsAndPersistence(t *testing.T) {
	base, _, _ := nativeFixture(t)
	for _, test := range []struct {
		data      string
		lfs       bool
		checks    bool
		threshold int
	}{
		{`{}`, true, true, 50},
		{`{"lfsIntegration":false,"largeFileChecks":false,"largeFileMiB":125}`, false, false, 125},
		{`{"lfsIntegration":true,"largeFileChecks":true,"largeFileMiB":0}`, true, true, 50},
	} {
		state := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(state, []byte(test.data), 0600); err != nil {
			t.Fatal(err)
		}
		app := newNativeApp(base.service, state)
		t.Cleanup(app.cancel)
		if app.prefs.LFSIntegration != test.lfs || app.prefs.LargeFileChecks != test.checks || app.prefs.LargeFileMiB != test.threshold {
			t.Fatalf("默认值或已有关闭值未保留：%+v", app.prefs)
		}
		if err := app.savePreferences(); err != nil {
			t.Fatal(err)
		}
		restored := newNativeApp(base.service, state)
		t.Cleanup(restored.cancel)
		if restored.gitFeatures("") != app.gitFeatures("") {
			t.Fatal("重启后全局配置发生变化")
		}
	}
}

func TestNativeRepositoryGitFeatureInheritanceAndRollback(t *testing.T) {
	app, path, _ := nativeFixture(t)
	enabled, disabled, threshold := true, false, 75
	app.setRepositoryGitOverride(path, nativeGitOverride{LFS: &disabled, Checks: &enabled, Threshold: &threshold})
	app.prefs.LFSIntegration, app.prefs.LargeFileChecks, app.prefs.LargeFileMiB = true, false, 100
	effective := app.gitFeatures(path)
	if effective.lfs || !effective.checks || effective.threshold != 75<<20 {
		t.Fatal("仓库覆盖没有独立生效")
	}
	app.setRepositoryGitOverride(path, nativeGitOverride{Checks: &disabled, Threshold: &threshold})
	if app.gitFeatures(path).checks || app.gitFeatures(path).threshold != 75<<20 {
		t.Fatal("关闭检查后不应删除阈值")
	}
	app.setRepositoryGitOverride(path, nativeGitOverride{})
	if _, ok := app.prefs.RepositoryGit[path]; ok {
		t.Fatal("恢复继承必须删除覆盖，而非写入当前全局值")
	}
	if !app.gitFeatures(path).lfs || app.gitFeatures(path).checks || app.gitFeatures(path).threshold != 100<<20 {
		t.Fatal("继承没有读取当前全局设置")
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	app.stateFile = filepath.Join(blocker, "state.json")
	app.setRepositoryGitOverride(path, nativeGitOverride{LFS: &disabled})
	if !app.gitFeatures(path).lfs || app.settingsError == "" {
		t.Fatal("保存失败没有还原仓库设置")
	}
}

func TestNativeLargeFileChecksClassification(t *testing.T) {
	report := gitrepo.FileInspectionReport{Files: []gitrepo.FileInspection{
		{Path: "large.bin", Size: 50 << 20, Binary: true, Fingerprint: "one"},
		{Path: "large.txt", Size: 50 << 20},
		{Path: "small.png", Size: 1024, Binary: true},
		{Path: "tracked.bin", Size: 50 << 20, Binary: true, LFS: true},
		{Path: "pointer", Size: 50 << 20, Pointer: true},
	}}
	if risks := largeFileRisks(report, 50<<20, false, nil); len(risks) != 2 {
		t.Fatalf("暂存前应只提示大二进制和大文本：%+v", risks)
	}
	if risks := largeFileRisks(report, 50<<20, true, map[string]string{"large.bin": "one"}); len(risks) != 2 {
		t.Fatalf("提交前应跳过同一已确认 blob，发现未转换 LFS 内容：%+v", risks)
	}
}

func TestNativeLargeFileStageReviewCancelAndAcknowledgement(t *testing.T) {
	app, path, finish := nativeFixture(t)
	path = app.repository.snapshot.Path
	app.screen = "changes"
	app.prefs.LargeFileMiB = 1
	writeNativeFile(t, path, "large.bin", "\x00"+strings.Repeat("x", 1<<20))
	writeNativeFile(t, path, "small.txt", "small")
	app.stagePaths(false, []string{"large.bin", "small.txt"})
	finish()
	if app.dialog == nil || app.dialog.title != locales.StageReviewTitle() {
		t.Fatalf("未汇总为一个检查弹窗：%s", app.problem)
	}
	if staged := nativeGit(t, path, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("用户确认前索引被修改：%s", staged)
	}
	dialog := app.dialog
	tester := ui.NewTester(app.view, 1024, 680)
	if !tester.HasText(locales.NormalFilesSummary("1")) || !tester.HasText("large.bin") {
		t.Fatal("弹窗缺少风险文件或正常文件摘要")
	}
	app.closeDialog(dialog)
	if app.dialog != nil || nativeGit(t, path, "diff", "--cached", "--name-only") != "" {
		t.Fatal("取消检查没有保留索引")
	}
	app.stagePaths(false, []string{"large.bin", "small.txt"})
	finish()
	app.dialog.secondarySubmit()
	finish()
	if app.problem != "" || app.dialog != nil {
		t.Fatalf("普通暂存失败：%s", app.problem)
	}
	if len(app.largeFileAcknowledged[path]) != 1 {
		t.Fatal("没有记录已确认的暂存内容")
	}
	app.prefs.Drafts[path] = "确认后的大文件"
	app.commit()
	finish()
	if app.dialog != nil || app.problem != "" || nativeGit(t, path, "log", "-1", "--format=%s") != "确认后的大文件" {
		t.Fatalf("同一暂存内容不应再次提示：%s", app.problem)
	}
}

func TestNativeLargeFileCommitFallbackAndStaleIndex(t *testing.T) {
	app, path, finish := nativeFixture(t)
	path = app.repository.snapshot.Path
	app.screen = "changes"
	app.prefs.LargeFileMiB = 1
	app.prefs.LFSIntegration = false
	writeNativeFile(t, path, "outside.bin", "\x00"+strings.Repeat("x", 1<<20))
	nativeGit(t, path, "add", "outside.bin")
	writeNativeFile(t, path, "outside.bin", "small worktree")
	app.mutate("", nil)
	finish()
	app.prefs.Drafts[path] = "外部暂存"
	app.commit()
	finish()
	if app.dialog == nil || app.dialog.title != locales.CommitReviewTitle() {
		t.Fatalf("提交兜底没有检查实际暂存 blob：%s", app.problem)
	}
	app.dialog.submit(app.dialog)
	finish()
	if app.problem != "" || app.prefs.Drafts[path] != "" {
		t.Fatalf("用户确认后的提交失败：%s", app.problem)
	}
	writeNativeFile(t, path, "outside.bin", "\x00"+strings.Repeat("y", 1<<20))
	nativeGit(t, path, "add", "outside.bin")
	app.mutate("", nil)
	finish()
	app.prefs.Drafts[path] = "保留草稿"
	app.commit()
	finish()
	dialog := app.dialog
	if dialog == nil {
		t.Fatal("变化后的内容必须重新确认")
	}
	writeNativeFile(t, path, "new.txt", "index changed")
	nativeGit(t, path, "add", "new.txt")
	dialog.submit(dialog)
	finish()
	if app.problem == "" || app.prefs.Drafts[path] != "保留草稿" {
		t.Fatal("索引变化后仍然提交或丢失草稿")
	}
}

func TestNativeLargeFileDisabledAndSmallBinary(t *testing.T) {
	for _, disabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "disabled", false: "small"}[disabled], func(t *testing.T) {
			app, path, finish := nativeFixture(t)
			app.prefs.LargeFileMiB = 1
			app.prefs.LargeFileChecks = !disabled
			data := "\x00small"
			if disabled {
				data = "\x00" + strings.Repeat("x", 1<<20)
			}
			writeNativeFile(t, path, "binary.dat", data)
			app.stagePaths(false, []string{"binary.dat"})
			finish()
			if app.dialog != nil || app.problem != "" || nativeGit(t, path, "diff", "--cached", "--name-only") != "binary.dat" {
				t.Fatalf("关闭检查或小二进制不应弹窗：%s", app.problem)
			}
		})
	}
}

func TestNativeRepositoryGitSettingsControls(t *testing.T) {
	app, _, finish := nativeIdentityFixture(t)
	path := app.repository.snapshot.Path
	app.repositorySettingsDialog()
	finish()
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click("Git / LFS"); err != nil {
		t.Fatal(err)
	}
	clickMenu := func(name string) {
		t.Helper()
		control, found := tester.AccessByRole(ui.RoleMenuButton, name)
		if !found || control.Offscreen {
			t.Fatalf("仓库设置菜单不可见：%s", name)
		}
		tester.ClickAt(control.Bounds.X+control.Bounds.W/2, control.Bounds.Y+control.Bounds.H/2)
	}
	clickMenu("Git LFS")
	if err := tester.ChooseMenuItem(locales.FeatureDisabled()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if app.gitFeatures(path).lfs || !app.prefs.LFSIntegration {
		t.Fatal("仓库设置错误修改了全局开关")
	}
	clickMenu("Git LFS")
	if err := tester.ChooseMenuItem(locales.InheritEnabled()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if app.prefs.RepositoryGit[path].LFS != nil {
		t.Fatal("选择继承时写入了仓库覆盖")
	}
	clickMenu(locales.LargeFileThreshold())
	if err := tester.ChooseMenuItem(locales.CustomThreshold()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	dialog := app.dialog
	dialog.fields[0].value = "0"
	dialog.submit(dialog)
	if dialog.problem == "" || app.gitFeatures(path).threshold != 50<<20 {
		t.Fatal("阈值应拒绝零且保留输入")
	}
	dialog.fields[0].value = "80"
	dialog.submit(dialog)
	if app.dialog == dialog || app.gitFeatures(path).threshold != 80<<20 {
		t.Fatal("自定义阈值没有保存并返回父设置")
	}
}

func TestNativeLargeFileReviewLayout(t *testing.T) {
	app, _, _ := nativeFixture(t)
	path := app.repository.snapshot.Path
	risks := []gitrepo.FileInspection{}
	for index := range 20 {
		risks = append(risks, gitrepo.FileInspection{
			Path: fmt.Sprintf("assets/%s/large-%d.bin", strings.Repeat("很长的目录名称-", 12), index),
			Size: 70 << 20, Binary: index%2 == 0,
		})
	}
	report := gitrepo.FileInspectionReport{Files: risks}
	for _, size := range [][2]int{{1024, 680}, {780, 480}} {
		for _, dark := range []bool{false, true} {
			app.largeFileReview(path, []string{"a", "b"}, report, risks, app.gitFeatures(path), "")
			tester := ui.NewTester(app.view, size[0], size[1])
			tester.SetDark(dark)
			tester.Frame()
			panel, found := tester.AccessByRole(ui.RoleDialog, "")
			if !found || panel.Bounds.Y < 0 || panel.Bounds.Y+panel.Bounds.H > float32(size[1]) {
				t.Fatalf("检查弹窗超出窗口 %v：%+v", size, panel)
			}
			for _, label := range []string{locales.Cancel(), locales.StageNormally(), locales.StageWithLFS()} {
				control, found := tester.AccessByRole(ui.RoleButton, label)
				if !found || control.Offscreen || control.Bounds.X < panel.Bounds.X || control.Bounds.X+control.Bounds.W > panel.Bounds.X+panel.Bounds.W {
					t.Fatalf("检查操作不可达 %s：%+v", label, control)
				}
			}
			if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
				saveNativeScreenshot(t, tester, directory, fmt.Sprintf("large-file-review-%d-dark-%t.png", size[1], dark))
			}
		}
	}
}

func TestNativeLFSOperationCanCancelInsideSettings(t *testing.T) {
	app, _, finish := nativeIdentityFixture(t)
	app.repositorySettingsDialog()
	finish()
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click("Git / LFS"); err != nil {
		t.Fatal(err)
	}
	parent := app.dialog
	app.runLFSOperation(parent, locales.DownloadingLFS(), func(ctx context.Context, _ *gitrepo.Repository) error {
		<-ctx.Done()
		return ctx.Err()
	})
	tester.Frame()
	button, found := tester.AccessByRole(ui.RoleButton, locales.CancelOperation())
	if !found || button.Disabled || button.Offscreen {
		t.Fatal("设置模态框内没有可用的取消操作")
	}
	if err := tester.Click(locales.CancelOperation()); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if app.busy || app.operationCancel != nil || app.dialog != parent || parent.problem == "" || !app.writable {
		t.Fatal("取消后没有保留设置与恢复可用状态")
	}
}

func TestNativeLFSDownloadAvailabilityAndFeatureScope(t *testing.T) {
	app, _, finish := nativeIdentityFixture(t)
	path := app.repository.snapshot.Path
	app.repositorySettingsDialog()
	finish()
	app.lfsState = &nativeLFSState{
		path: path, status: gitrepo.LFSStatus{Installed: true, Files: []gitrepo.LFSFile{{Path: "image.bin", Downloaded: true}}},
	}
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click("Git / LFS"); err != nil {
		t.Fatal(err)
	}
	button, found := tester.AccessByRole(ui.RoleButton, locales.DownloadLFS())
	if !found || !button.Disabled {
		t.Fatal("内容完整时不应允许重复下载")
	}
	app.lfsState.status.Files[0].Downloaded = false
	tester.Frame()
	if button, found := tester.AccessByRole(ui.RoleButton, locales.DownloadLFS()); !found || button.Disabled {
		t.Fatal("缺失内容时没有可用下载入口")
	}
	before, err := os.ReadFile(filepath.Join(path, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	app.prefs.LFSIntegration = false
	tester.Frame()
	if _, found := tester.AccessByRole(ui.RoleButton, locales.CheckLFSStatus()); found || !app.gitFeatures(path).checks {
		t.Fatal("LFS 开关未隐藏管理操作或错误关闭独立大文件检查")
	}
	after, err := os.ReadFile(filepath.Join(path, ".git", "config"))
	if err != nil || string(before) != string(after) {
		t.Fatal("客户端功能开关不应修改已有 Git 配置")
	}
}
