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

func TestNativeHistoryCommitActions(t *testing.T) {
	app, path, finish := nativeFixture(t)
	initial := app.repository.snapshot.Head
	nativeGit(t, path, "checkout", "-b", "topic")
	writeNativeFile(t, path, "hello.txt", "topic\n")
	nativeGit(t, path, "commit", "-am", "pick me")
	source := nativeGit(t, path, "rev-parse", "HEAD")
	nativeGit(t, path, "checkout", "main")
	nativeGit(t, path, "commit", "--allow-empty", "-m", "main head")
	head := nativeGit(t, path, "rev-parse", "HEAD")
	app.refresh()
	finish()
	app.screen = "history"
	app.searchHistory(false)
	finish()
	app.selectCommit(head)
	finish()
	tester := ui.NewTester(app.view, 1024, 680)
	click := func(label string) {
		t.Helper()
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	confirm := func(label string) {
		t.Helper()
		button, found := tester.AccessByRole(ui.RoleButton, label)
		if !found || button.Disabled || button.Offscreen {
			t.Fatalf("confirmation button %s not available: %+v", label, button)
		}
		tester.ClickAt(button.Bounds.X+button.Bounds.W/2, button.Bounds.Y+button.Bounds.H/2)
		if !app.busy {
			t.Fatalf("confirmation started no task: %+v", app.dialog)
		}
		finish()
	}
	choose := func(row, label string) {
		t.Helper()
		if err := tester.RightClick(row); err != nil {
			t.Fatal(err)
		}
		if err := tester.ChooseMenuItem(label); err != nil {
			t.Fatal(err)
		}
	}
	for _, dark := range []bool{false, true} {
		tester.SetDark(dark)
		for _, control := range []struct {
			role  ui.Role
			label string
		}{
			{ui.RoleButton, locales.CopyWorktreeCommit()},
			{ui.RoleButton, locales.CompareCommitWithHead()},
			{ui.RoleMenuButton, locales.CommitActions()},
		} {
			info, found := tester.AccessByRole(control.role, control.label)
			if !found || info.Offscreen || info.Bounds.W <= 0 || info.Bounds.X+info.Bounds.W > 1024 {
				t.Fatalf("control %s clipped: %+v", control.label, info)
			}
		}
		if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
			appearance := "light"
			if dark {
				appearance = "dark"
			}
			saveNativeScreenshot(t, tester, directory, "history-actions-"+appearance+".png")
		}
	}
	app.writable = false
	tester.Frame()
	click(locales.CopyWorktreeCommit())
	if tester.Clipboard() != head {
		t.Fatalf("detail copied %q, want %s", tester.Clipboard(), head)
	}
	choose("初始提交", locales.CopyWorktreeCommit())
	if tester.Clipboard() != initial || app.commitHash != head {
		t.Fatalf("row copy used the wrong commit: clipboard=%q selected=%s", tester.Clipboard(), app.commitHash)
	}
	choose("pick me", locales.CompareCommitWithHead())
	finish()
	finish()
	if app.screen != "compare" || app.comparison.result == nil || app.comparison.result.From != source ||
		app.comparison.result.Target != head || app.comparison.mergeBase {
		t.Fatalf("unexpected compare target: %+v", app.comparison)
	}
	app.writable = true
	app.switchWorkspace("history")
	tester.Frame()
	choose("pick me", locales.CherryPickCommitMenu())
	if app.dialog == nil || !strings.Contains(app.dialog.detail, shortHash(source)) ||
		!strings.Contains(app.dialog.detail, locales.CommitActionTarget("main")) {
		t.Fatalf("wrong cherry-pick confirmation: %+v", app.dialog)
	}
	confirm(locales.CherryPick())
	picked := nativeGit(t, path, "rev-parse", "HEAD")
	if picked == head || nativeGit(t, path, "show", "HEAD:hello.txt") != "topic" {
		t.Fatalf("cherry-pick failed: %s", app.problem)
	}
	app.selectCommit(picked)
	finish()
	tester.Frame()
	click(locales.CommitActions())
	if err := tester.ChooseMenuItem(locales.RevertCommitMenu()); err != nil {
		t.Fatal(err)
	}
	confirm(locales.RevertCommit())
	if nativeGit(t, path, "rev-parse", "HEAD^") != picked || nativeGit(t, path, "show", "HEAD:hello.txt") != "原始内容" {
		t.Fatalf("revert failed: %s", app.problem)
	}
}
