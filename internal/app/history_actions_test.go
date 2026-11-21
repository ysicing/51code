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

func TestNativeCommitDetailsNavigateMergeParents(t *testing.T) {
	app, path, finish := nativeFixture(t)
	root := app.repository.snapshot.Head
	nativeGit(t, path, "switch", "-c", "topic")
	writeNativeFile(t, path, "topic.txt", "topic\n")
	nativeGit(t, path, "add", "topic.txt")
	nativeGit(t, path, "commit", "-m", "topic parent")
	topic := nativeGit(t, path, "rev-parse", "HEAD")
	nativeGit(t, path, "switch", "main")
	writeNativeFile(t, path, "main.txt", "main\n")
	nativeGit(t, path, "add", "main.txt")
	nativeGit(t, path, "commit", "-m", "main parent")
	main := nativeGit(t, path, "rev-parse", "HEAD")
	nativeGit(t, path, "merge", "--no-ff", "-m", "merge topic", "topic")
	nativeGit(t, path, "commit", "--amend", "--no-edit", "--date=2026-11-20T09:08:07+05:30")
	merge := nativeGit(t, path, "rev-parse", "HEAD")
	app.refresh()
	finish()
	app.switchWorkspace("history")
	app.searchHistory(false)
	finish()
	app.selectCommit(merge)
	finish()

	tester := ui.NewTester(app.view, 1024, 680)
	for _, dark := range []bool{false, true} {
		tester.SetDark(dark)
		for _, text := range []string{
			locales.FullCommitID(), "[" + shortHash(merge) + "]",
			"Native Test <native@example.test>", "2026-11-20 09:08:07 +05:30",
		} {
			if !tester.HasText(text) {
				t.Fatalf("commit detail missing %q", text)
			}
		}
		for _, parent := range []string{main, topic} {
			link, found := tester.AccessByRole(ui.RoleButton, shortHash(parent))
			if !found || link.Disabled || link.Offscreen || link.Bounds.X+link.Bounds.W > 1024 {
				t.Fatalf("merge parent link unavailable: %s %+v", parent, link)
			}
		}
	}
	if err := tester.Click(locales.CopyWorktreeCommit()); err != nil || tester.Clipboard() != merge {
		t.Fatalf("copy full merge SHA = %q, %v", tester.Clipboard(), err)
	}
	clickParent := func(hash, file string) {
		t.Helper()
		link, found := tester.AccessByRole(ui.RoleButton, shortHash(hash))
		if !found || link.Disabled || link.Offscreen {
			t.Fatalf("parent link unavailable: %s %+v", hash, link)
		}
		tester.ClickAt(link.Bounds.X+link.Bounds.W/2, link.Bounds.Y+link.Bounds.H/2)
		finish()
		tester.Frame()
		if app.historySelected < 0 || app.history[app.historySelected].Hash != hash ||
			app.commitHash != hash || app.commitFile != file {
			t.Fatalf("jump to %s selected %s at row %d, file %q", hash, app.commitHash, app.historySelected, app.commitFile)
		}
	}
	clickParent(main, "main.txt")
	clickParent(root, "hello.txt")
	if tester.HasText(locales.ParentCommits()) {
		t.Fatal("root commit shows a parent row")
	}
	app.selectCommit(merge)
	finish()
	app.history = app.history[:1]
	app.historyMore = false
	tester.Frame()
	clickParent(topic, "topic.txt")
	if nativeGit(t, path, "rev-parse", "HEAD") != merge {
		t.Fatal("parent navigation changed the checked-out commit")
	}
}

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
