// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestNativeGitMissingCanChooseAndRecover(t *testing.T) {
	app, _, finish := nativeIdentityFixture(t)
	actual, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gitrepo.ConfigureExecutable(gitrepo.ExecutableOptions{Source: "system"}) })
	app.prefs.Git = gitrepo.ExecutableOptions{Source: "custom", Path: filepath.Join(t.TempDir(), "missing-git")}
	tester := sidebarSettingsTester(t, app)
	if err := tester.Click("Git"); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.SetSize(780, 480)
	for _, dark := range []bool{false, true} {
		tester.SetDark(dark)
		tester.Frame()
		for _, label := range []string{locales.GitUnavailable(), locales.GitSource(), locales.DetectGit()} {
			if _, found := tester.Find(label); !found {
				t.Fatalf("缺失 Git 时没有恢复入口 %s", label)
			}
		}
		if _, found := tester.Find(locales.GlobalGitScope()); found {
			t.Fatal("Git 不可用时仍加载提交身份")
		}
		if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
			name := "git-missing-light.png"
			if dark {
				name = "git-missing-dark.png"
			}
			saveNativeScreenshot(t, tester, directory, name)
		}
	}
	app.openDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{actual}, nil }
	clickNativeSettingsControl(t, tester, ui.RoleMenuButton, locales.GitSource())
	if err := tester.ChooseMenuItem(locales.ChooseGit()); err != nil {
		t.Fatal(err)
	}
	finish()
	finish()
	tester.Frame()
	if app.identityEditor.gitError != "" || !app.identityEditor.ready || app.prefs.Git.Path != actual {
		t.Fatalf("未恢复 Git：%+v %s", app.prefs.Git, app.identityEditor.gitError)
	}
	restored := newNativeApp(app.service, app.stateFile)
	defer restored.cancel()
	if restored.prefs.Git != app.prefs.Git {
		t.Fatal("Git 选择未持久化")
	}
	if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
		tester.SetSize(1024, 680)
		tester.SetDark(false)
		saveNativeScreenshot(t, tester, directory, "git-source-light.png")
		tester.SetDark(true)
		saveNativeScreenshot(t, tester, directory, "git-source-dark.png")
	}
}

func TestNativeGitSelectionPreservesDraftAndRollsBack(t *testing.T) {
	app, _, finish := nativeIdentityFixture(t)
	tester := sidebarSettingsTester(t, app)
	if err := tester.Click("Git"); err != nil {
		t.Fatal(err)
	}
	finish()
	editor, dialog := app.identityEditor, app.dialog
	previous := app.prefs.Git
	editor.name = "Unsaved identity"
	app.selectGitExecutable("system", "", editor, dialog)
	if app.busy || editor.name != "Unsaved identity" || app.prefs.Git != previous {
		t.Fatal("选择 Git 丢失了身份草稿")
	}
	editor.adopt(editor.settings)
	app.stateFile = filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(app.stateFile, 0700); err != nil {
		t.Fatal(err)
	}
	actual, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	app.selectGitExecutable("custom", actual, editor, dialog)
	finish()
	if app.prefs.Git != previous || dialog.problem == "" || !editor.ready {
		t.Fatal("保存失败没有保留原 Git 和身份")
	}
	app.selectGitExecutable("custom", filepath.Join(t.TempDir(), "missing"), editor, dialog)
	finish()
	if app.prefs.Git != previous || dialog.problem == "" {
		t.Fatal("无效 Git 替换了现有选择")
	}
}
