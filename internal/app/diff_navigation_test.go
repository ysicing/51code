// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestNativeDiffNavigationSkipsReviewedFiles(t *testing.T) {
	for _, layout := range []string{"unified", "split"} {
		t.Run(layout, func(t *testing.T) {
			app, path, finish := nativeFixture(t)
			var source strings.Builder
			for line := 1; line <= 35; line++ {
				fmt.Fprintf(&source, "sample %02d\n", line)
			}
			oldText := source.String()
			changed := strings.ReplaceAll(strings.ReplaceAll(oldText, "sample 01\n", "first change\n"), "sample 35\n", "last change\n")
			for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
				writeNativeFile(t, path, name, oldText)
			}
			nativeGit(t, path, "add", ".")
			nativeGit(t, path, "commit", "-m", "base")
			base := nativeGit(t, path, "rev-parse", "HEAD")
			for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
				writeNativeFile(t, path, name, changed)
			}
			nativeGit(t, path, "commit", "-am", "changes")
			app.switchWorkspace("compare")
			app.comparison.base, app.comparison.target = base, "HEAD"
			app.startComparison()
			finish()
			app.prefs.DiffLayout = layout
			app.reviewedFiles = map[revisionReviewScope]map[string]bool{app.revisionReviewScope(): {"b.txt": true}}
			tester := ui.NewTester(app.view, 1150, 720)
			if len(app.hunkOffsets) != 2 {
				t.Fatalf("expected two patch hunks, got %v", app.hunkOffsets)
			}
			app.navigateDiffHunk(0)
			tester.Frame()
			tester.Key(0, ui.KeyF7)
			if app.hunk != 1 || app.readTask != nil {
				t.Fatalf("F7 did not select the next hunk: %d", app.hunk)
			}
			tester.Key(0, ui.KeyF7)
			if !tester.HasText(locales.NextDiffFileHint()) || app.readTask != nil {
				t.Fatal("first key at the boundary should show a hint")
			}
			tester.Key(0, ui.KeyF7)
			finish()
			tester.Frame()
			if app.comparison.file != "c.txt" || app.hunk != 0 {
				t.Fatalf("did not land on the first hunk of c.txt: %q/%d", app.comparison.file, app.hunk)
			}
			tester.Key(ui.Shift, ui.KeyF7)
			if !tester.HasText(locales.PreviousDiffFileHint()) {
				t.Fatal("reverse step did not show its boundary hint")
			}
			tester.Key(ui.Shift, ui.KeyF7)
			finish()
			tester.Frame()
			if app.comparison.file != "a.txt" || app.hunk != 1 {
				t.Fatalf("did not land on the last hunk of a.txt: %q/%d", app.comparison.file, app.hunk)
			}
		})
	}
}

func TestNativeDiffFileFinderKeepsComparisonAndRevealsTreeFile(t *testing.T) {
	app, path, finish := nativeFixture(t)
	base := app.repository.snapshot.Head
	if err := os.Mkdir(filepath.Join(path, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api/actions.go", "api/router.go", "z.txt"} {
		writeNativeFile(t, path, name, "sample\n")
	}
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "new files")
	app.switchWorkspace("compare")
	app.comparison.base, app.comparison.target = base, "HEAD"
	app.startComparison()
	finish()
	comparison := app.comparison.result
	app.prefs.FileTree = true
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click("api/"); err != nil {
		t.Fatal(err)
	}
	tester.Key(ui.Cmd|ui.Shift, ui.KeyP)
	if app.dialog == nil || app.dialog.title != locales.GoToChangedFile() {
		t.Fatal("shortcut did not open the contextual finder")
	}
	tester.Type("API .GO")
	tester.Key(0, ui.KeyDown)
	tester.Key(0, ui.KeyEnter)
	finish()
	tester.Frame()
	if app.screen != "compare" || app.comparison.result != comparison || app.comparison.file != "api/router.go" {
		t.Fatalf("finder changed the comparison or selected %q", app.comparison.file)
	}
	if row, ok := tester.AccessByRole(ui.RoleTreeItem, "api/router.go"); !ok || !row.Selected || row.Offscreen {
		t.Fatalf("finder did not reveal the selected file: %+v", row)
	}
	if err := tester.Click(locales.GoToChangedFile()); err != nil {
		t.Fatal(err)
	}
	tester.Type("not-a-result")
	if !tester.HasText(locales.NoMatchingFiles()) {
		t.Fatal("empty search did not show feedback")
	}
	tester.Key(0, ui.KeyEnter)
	if app.dialog == nil || app.readTask != nil {
		t.Fatal("empty Enter should keep the finder open")
	}
	tester.Key(0, ui.KeyEscape)
	if app.dialog != nil || app.comparison.file != "api/router.go" {
		t.Fatal("canceling should preserve the selected file")
	}
	tester.Key(ui.Cmd|ui.Shift, ui.KeyP)
	tester.Type("z.txt")
	tester.Key(0, ui.KeyEnter)
	finish()
	if app.comparison.file != "z.txt" || app.comparison.result != comparison {
		t.Fatal("Enter did not select the first result after filtering")
	}
}

func TestNativeDiffFileFinderDistinguishesStagingAndHonorsFilters(t *testing.T) {
	app, path, finish := nativeFixture(t)
	writeNativeFile(t, path, "hello.txt", "staged\n")
	nativeGit(t, path, "add", "hello.txt")
	writeNativeFile(t, path, "hello.txt", "working\n")
	writeNativeFile(t, path, "other.txt", "excluded\n")
	app.refresh()
	finish()
	app.fileQuery = "hello"
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click(locales.GoToChangedFile()); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{locales.StagedFiles(), locales.UnstagedFiles()} {
		if _, ok := tester.AccessByRole(ui.RoleListItem, "hello.txt · "+label); !ok {
			t.Fatalf("missing %s result", label)
		}
	}
	if _, ok := tester.AccessByRole(ui.RoleListItem, "other.txt · "+locales.UnstagedFiles()); ok {
		t.Fatal("finder ignored the active file filter")
	}
	if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
		for _, dark := range []bool{false, true} {
			tester.SetDark(dark)
			saveNativeScreenshot(t, tester, directory, fmt.Sprintf("diff-file-finder-dark-%t.png", dark))
		}
	}
	if err := tester.Click("hello.txt · " + locales.StagedFiles()); err != nil {
		t.Fatal(err)
	}
	finish()
	if app.selectedFile != "hello.txt" || !app.staged || app.diff.New.Text != "staged\n" {
		t.Fatal("finder did not select the staged version")
	}
}
