// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func TestNativeWorktreeSearchOpensMatchAndKeepsResults(t *testing.T) {
	app, path, finish := nativeFixture(t)
	filename := strings.Repeat("long-", 18) + "source.txt"
	writeNativeFile(t, path, filename, "before\n前缀 needle\n")
	app.switchWorkspace("search")
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click(locales.WorktreeSearch()); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click(locales.SearchWorktreeText()); err != nil {
		t.Fatal(err)
	}
	tester.Type("missing needle")
	tester.Key(0, ui.KeyEnter)
	finish()
	tester.Frame()
	if !tester.HasText(locales.NoWorktreeSearchMatches()) {
		t.Fatal("no-match state was not displayed")
	}
	if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
		saveNativeScreenshot(t, tester, directory, "worktree-search-empty.png")
	}
	tester.Key(ui.Cmd, ui.KeyA)
	tester.Type("needle")
	tester.Key(0, ui.KeyEnter)
	finish()
	finish()
	tester.Frame()
	if len(app.contentSearch.results) != 1 || !tester.HasText("前缀 needle") {
		t.Fatalf("search results = %+v, query = %q", app.contentSearch.results, app.contentSearch.query)
	}
	for _, dark := range []bool{false, true} {
		tester.SetDark(dark)
		for _, label := range []string{locales.SearchWorktreeText(), "前缀 needle"} {
			bounds, ok := tester.Find(label)
			if !ok || bounds.X+bounds.W > 1024 || bounds.Y+bounds.H > 680 {
				t.Fatalf("clipped search control %q: %+v", label, bounds)
			}
		}
		if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
			name := "worktree-search-light.png"
			if dark {
				name = "worktree-search-dark.png"
			}
			saveNativeScreenshot(t, tester, directory, name)
		}
	}
	if err := tester.Click("前缀 needle"); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if app.screen != "files" || app.browser.file != filename {
		t.Fatalf("opened screen %q", app.screen)
	}
	if position := app.browser.viewer.state(1).CursorPos(); position != (editor.Pos{Line: 1, Col: 7}) {
		t.Fatalf("cursor = %+v", position)
	}
	app.switchWorkspace("search")
	tester.Frame()
	if app.searchMode != nativeSearchContent || !tester.HasText("前缀 needle") {
		t.Fatal("returning from the preview discarded search results")
	}
	writeNativeFile(t, path, filename, "updated text\n")
	app.refresh()
	for app.busy || app.readTask != nil {
		finish()
	}
	tester.Frame()
	if app.contentSearch.query != "needle" || !tester.HasText(locales.NoWorktreeSearchMatches()) || tester.HasText("前缀 needle") {
		t.Fatal("refresh did not re-run the submitted content query")
	}
	if err := tester.Click(locales.SearchWorktreeText()); err != nil {
		t.Fatal(err)
	}
	tester.Key(ui.Cmd, ui.KeyA)
	tester.Type("updated")
	app.refresh()
	for app.busy || app.readTask != nil {
		finish()
	}
	if app.contentSearch.query != "updated" || app.contentSearch.searched {
		t.Fatal("refresh should preserve an unsubmitted content query")
	}
}

func TestNativeQuickOpenEnterWhileSearching(t *testing.T) {
	for _, changeQuery := range []bool{false, true} {
		app, _, finish := nativeFixture(t)
		app.openQuickSearch()
		tester := ui.NewTester(app.view, 1024, 680)
		if err := tester.Click(locales.FilterRepositoryFiles()); err != nil {
			t.Fatal(err)
		}
		tester.Type("hello")
		tester.Key(0, ui.KeyEnter)
		if changeQuery {
			tester.Type(".txt")
		}
		for app.readTask != nil {
			finish()
		}
		if changeQuery {
			if app.screen != "search" || len(app.pathSearch.results) != 1 {
				t.Fatal("editing the query should cancel the pending open")
			}
		} else if app.screen != "files" || app.browser.file != "hello.txt" {
			t.Fatal("one Enter should open the matching file when results arrive")
		}
	}
}

func TestNativeQuickOpenRanksAndOpensFilesByKeyboard(t *testing.T) {
	app, path, finish := nativeFixture(t)
	writeNativeFile(t, path, "browser.go", "package app\n")
	writeNativeFile(t, path, "binary-write.go", "package app\n")
	trace := filepath.Join(t.TempDir(), "git-trace")
	t.Setenv("GIT_TRACE", trace)
	checkPathReads := func(want int) {
		t.Helper()
		log, err := os.ReadFile(trace)
		if err != nil {
			t.Fatal(err)
		}
		if count := strings.Count(string(log), "ls-files -z --cached --others --exclude-standard --"); count != want {
			t.Fatalf("path list reads = %d, want %d: %s", count, want, log)
		}
	}
	app.openQuickSearch()
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click(locales.FilterRepositoryFiles()); err != nil {
		t.Fatal(err)
	}
	tester.Type("brw")
	finish()
	writeNativeFile(t, path, "new-browser-window.go", "package app\n")
	tester.Type(" .go")
	finish()
	tester.Frame()
	if len(app.pathSearch.results) != 2 || app.pathSearch.results[0].Path != "browser.go" {
		t.Fatalf("quick open results = %+v", app.pathSearch.results)
	}
	checkPathReads(1)
	app.refresh()
	for app.busy || app.readTask != nil {
		finish()
	}
	tester.Frame()
	if len(app.pathSearch.results) != 3 || app.pathSearch.query != "brw .go" {
		t.Fatalf("refresh did not update quick open results: %+v", app.pathSearch.results)
	}
	checkPathReads(2)
	if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
		saveNativeScreenshot(t, tester, directory, "quick-open.png")
	}
	tester.Key(0, ui.KeyDown)
	if app.pathSearch.selected != 1 {
		t.Fatalf("Down did not select second result: %d", app.pathSearch.selected)
	}
	tester.Key(0, ui.KeyUp)
	tester.Key(0, ui.KeyEnter)
	finish()
	tester.Frame()
	if app.screen != "files" || app.browser.file != "browser.go" {
		t.Fatal("Enter did not open the first fuzzy match")
	}
}
