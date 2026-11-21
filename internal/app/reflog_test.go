// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"strings"
	"testing"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestNativeRecoverLostCommitFromReflog(t *testing.T) {
	app, path, finish := nativeFixture(t)
	base := app.repository.snapshot.Head
	nativeGit(t, path, "switch", "-c", "deleted")
	writeNativeFile(t, path, "hello.txt", "recovered text\n")
	nativeGit(t, path, "commit", "-am", "需要恢复的提交")
	lost := nativeGit(t, path, "rev-parse", "HEAD")
	nativeGit(t, path, "switch", "main")
	nativeGit(t, path, "branch", "-D", "deleted")
	writeNativeFile(t, path, "hello.txt", "unsaved worktree changes\n")
	app.refresh()
	finish()

	tester := ui.NewTester(app.view, 1024, 680)
	click := func(label string) {
		t.Helper()
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	open := func() {
		t.Helper()
		tester.Frame()
		click(locales.More())
		if err := tester.ChooseMenuItem(locales.FindLostCommits()); err != nil {
			t.Fatal(err)
		}
		finish()
		tester.Frame()
	}
	app.writable = false
	open()
	click("需要恢复的提交")
	if action, ok := tester.AccessByRole(ui.RoleButton, locales.CreateRecoveryBranch()); !ok || !action.Disabled {
		t.Fatalf("recovery should be disabled in a read-only repository: %+v", action)
	}
	click(locales.CompareCommitWithHead())
	finish()
	if app.screen != "compare" || app.comparison.result == nil || app.comparison.result.From != lost || app.comparison.result.Target != base {
		t.Fatalf("compare did not use the reflog commit: %+v", app.comparison)
	}
	app.writable = true
	open()
	for _, dark := range []bool{false, true} {
		tester.SetDark(dark)
		if action, ok := tester.AccessByRole(ui.RoleButton, locales.CreateRecoveryBranch()); !ok || action.Offscreen || action.Bounds.X+action.Bounds.W > 1024 {
			t.Fatalf("recovery action is clipped: %+v", action)
		}
	}
	click("需要恢复的提交")
	if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
		saveNativeScreenshot(t, tester, directory, "reflog-recovery.png")
	}
	click(locales.CreateRecoveryBranch())
	dialog := app.dialog
	if dialog == nil || dialog.check || !strings.Contains(dialog.detail, shortHash(lost)) {
		t.Fatal("recovery did not open a creation-only branch form pinned to the selected SHA")
	}
	click(locales.BranchName())
	tester.Type("recovered")
	finish()
	tester.Frame()
	click(locales.Create())
	finish()
	if app.dialog != nil || app.problem != "" {
		t.Fatalf("recovery did not close the branch form: %s", app.problem)
	}
	if got := nativeGit(t, path, "rev-parse", "refs/heads/recovered"); got != lost {
		t.Fatalf("recovery branch = %s, want %s", got, lost)
	}
	if head := nativeGit(t, path, "rev-parse", "HEAD"); head != base || nativeGit(t, path, "status", "--short") != "M hello.txt" {
		t.Fatal("recovery changed HEAD or did not preserve worktree edits")
	}
	open()
	if !tester.HasText(locales.NoLostCommits()) {
		t.Fatal("recovered commit remains in the list")
	}
}

func TestNativeReflogCloseIgnoresCanceledRead(t *testing.T) {
	app, _, finish := nativeFixture(t)
	previous := make(chan struct{})
	app.readDone = previous
	app.lostCommitsDialog()
	first := app.dialog
	if first == nil || app.readTask == nil {
		close(previous)
		t.Fatal("reflog read did not start")
	}
	app.closeDialog(first)
	if app.dialog != nil || app.readTask != nil {
		close(previous)
		t.Fatal("closing reflog did not cancel its read")
	}
	app.lostCommitsDialog()
	second := app.dialog
	close(previous)
	finish()
	if app.dialog != second || second.problem != "" || app.readTask == nil {
		t.Fatal("canceled read changed the newly opened dialog")
	}
	finish()
	tester := ui.NewTester(app.view, 800, 520)
	if app.dialog != second || app.readTask != nil || app.problem != "" || !tester.HasText(locales.NoLostCommits()) {
		t.Fatal("reopened dialog did not complete its own read")
	}
	if action, ok := tester.AccessByRole(ui.RoleButton, locales.CreateRecoveryBranch()); !ok || !action.Disabled || action.Offscreen {
		t.Fatalf("empty recovery action = %+v", action)
	}
}
