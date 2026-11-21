// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
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
}

func TestNativeQuickOpenRanksAndOpensFilesByKeyboard(t *testing.T) {
	app, path, finish := nativeFixture(t)
	writeNativeFile(t, path, "browser.go", "package app\n")
	writeNativeFile(t, path, "binary-write.go", "package app\n")
	app.openQuickSearch()
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click(locales.FilterRepositoryFiles()); err != nil {
		t.Fatal(err)
	}
	tester.Type("brw .go")
	finish()
	tester.Frame()
	if len(app.pathSearch.results) != 2 || app.pathSearch.results[0].Path != "browser.go" {
		t.Fatalf("quick open results = %+v", app.pathSearch.results)
	}
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
