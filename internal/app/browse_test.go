// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func TestNativeBrowserReadsUnchangedAndNewFilesWithoutReplacingDiff(t *testing.T) {
	app, path, finish := nativeFixture(t)
	writeNativeFile(t, path, "new.go", "package main\n\nfunc main() {}\n")
	app.setDiff(gitrepo.Diff{
		Old: &gitrepo.FileContent{Path: "saved.txt", Revision: "INDEX", Present: true, Text: "before\n"},
		New: &gitrepo.FileContent{Path: "saved.txt", Revision: "WORKTREE", Present: true, Text: "after\n"},
	})
	diffBuffer := app.fileViewer.state(1).Buffer
	diff := app.diff
	app.switchWorkspace("files")
	finish()
	if !slices.Contains(app.browser.files, "hello.txt") || !slices.Contains(app.browser.files, "new.go") {
		t.Fatal("browser omitted unchanged or new file")
	}
	app.selectBrowserFile("new.go")
	finish()
	state := app.browser.viewer.state(1)
	browserBuffer := state.Buffer
	if state.Buffer == nil || !state.ReadOnly || state.Buffer.String() != "package main\n\nfunc main() {}\n" {
		t.Fatal("browser did not load read-only full file")
	}
	if app.fileViewer.states[1].Buffer != diffBuffer {
		t.Fatal("browser replaced the diff viewer state")
	}
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click(locales.FindInFile()); err != nil {
		t.Fatal(err)
	}
	tester.Type("main")
	if count, _ := state.FindMatches(); count != 2 {
		t.Fatal("browser find did not search full content")
	}
	if err := tester.Click(locales.FileContentLabel()); err != nil {
		t.Fatal(err)
	}
	tester.Type("must remain read-only")
	content, err := os.ReadFile(filepath.Join(path, "new.go"))
	if err != nil || string(content) != "package main\n\nfunc main() {}\n" ||
		state.Buffer.String() != string(content) {
		t.Fatal("browser edited the file or its read-only buffer", err)
	}
	app.switchWorkspace("changes")
	if app.diff.Old != diff.Old || app.diff.New != diff.New || app.selectedFile != "" {
		t.Fatal("returning from browser did not restore the existing diff")
	}
	app.switchWorkspace("files")
	finish()
	if app.browser.viewer.states[1].Buffer != browserBuffer {
		t.Fatal("returning to browser discarded viewer state")
	}
}

func TestNativeBrowserRefreshesTextAndListWithUnchangedStatus(t *testing.T) {
	app, path, finish := nativeFixture(t)
	app.switchWorkspace("files")
	finish()
	app.selectBrowserFile("hello.txt")
	finish()
	writeNativeFile(t, path, "hello.txt", "first change\n")
	app.refreshRepositoryView(false)
	finish()
	if got := app.browser.viewer.state(1).Buffer.String(); got != "first change\n" {
		t.Fatalf("first refresh = %q", got)
	}
	writeNativeFile(t, path, "hello.txt", "second change\n")
	writeNativeFile(t, path, "added.txt", "new\n")
	app.refreshRepositoryView(false)
	finish()
	if got := app.browser.viewer.state(1).Buffer.String(); got != "second change\n" {
		t.Fatalf("same-status refresh = %q", got)
	}
	if !slices.Contains(app.browser.files, "added.txt") {
		t.Fatal("external refresh omitted new file")
	}
	app.refresh()
	finish()
	if got := app.browser.viewer.state(1).Buffer.String(); got != "second change\n" {
		t.Fatalf("manual refresh lost content: %q", got)
	}
}

func TestNativeBrowserFailurePreservesViewerAndSelection(t *testing.T) {
	app, path, finish := nativeFixture(t)
	app.switchWorkspace("files")
	finish()
	app.selectBrowserFile("hello.txt")
	finish()
	buffer := app.browser.viewer.state(1).Buffer
	app.selectBrowserFile("missing.txt")
	finish()
	if app.browser.file != "hello.txt" || app.browser.viewer.state(1).Buffer != buffer {
		t.Fatal("failed selection cleared successful content")
	}
	app.dialog, app.problem = nil, ""
	if err := os.Rename(filepath.Join(path, ".git"), filepath.Join(path, ".hidden-git")); err != nil {
		t.Fatal(err)
	}
	app.refreshRepositoryView(false)
	finish()
	if app.browser.file != "hello.txt" || app.browser.viewer.state(1).Buffer != buffer {
		t.Fatal("failed external refresh cleared successful content")
	}
}

func TestNativeBrowserDeletedFollowsSelectedVersionAndFiltering(t *testing.T) {
	app, path, finish := nativeFixture(t)
	if err := os.Remove(filepath.Join(path, "hello.txt")); err != nil {
		t.Fatal(err)
	}
	app.switchWorkspace("files")
	finish()
	app.selectBrowserFile("hello.txt")
	finish()
	tester := ui.NewTester(app.browserWorkspace, 1024, 680)
	if _, found := tester.Find(locales.AbsentVersion()); !found {
		t.Fatal("deleted worktree file should show the absent-version state")
	}
	if _, found := tester.Access(locales.FileVersion()); found {
		t.Fatal("browser retained a separate preview version menu")
	}
	app.loadBrowser("HEAD", "hello.txt", nil)
	finish()
	tester.Frame()
	if got := app.browser.viewer.state(1).Buffer.String(); got != "原始内容\n" {
		t.Fatalf("HEAD content = %q", got)
	}
	app.browser.query = "HELLO"
	if got := app.browser.visibleFiles(); len(got) != 1 || got[0].Path != "hello.txt" {
		t.Fatalf("case-insensitive filtering = %+v", got)
	}
	app.browser.query = "absent"
	if len(app.browser.visibleFiles()) != 0 {
		t.Fatal("path filter did not hide nonmatching files")
	}
}

func TestNativeBrowserPreviewFollowsVersionMenu(t *testing.T) {
	app, path, finish := nativeFixture(t)
	writeNativeFile(t, path, "hello.txt", "index content\n")
	nativeGit(t, path, "add", "hello.txt")
	writeNativeFile(t, path, "hello.txt", "working content\n")
	app.switchWorkspace("files")
	finish()
	app.selectBrowserFile("hello.txt")
	finish()
	tester := ui.NewTester(app.browserWorkspace, 1024, 680)
	for _, test := range []struct {
		label, text string
	}{
		{"HEAD", "原始内容\n"},
		{locales.IndexVersion(), "index content\n"},
		{locales.WorkingVersion(), "working content\n"},
	} {
		menu, found := tester.AccessByRole(ui.RoleMenuButton, locales.RepositoryFiles())
		if !found {
			t.Fatal("browser version menu is missing")
		}
		tester.ClickAt(menu.Bounds.X+menu.Bounds.W/2, menu.Bounds.Y+menu.Bounds.H/2)
		if err := tester.ChooseMenuItem(test.label); err != nil {
			t.Fatal(err)
		}
		finish()
		app.selectBrowserFile("hello.txt")
		finish()
		tester.Frame()
		field, found := tester.AccessByRole(ui.RoleTextField, locales.FileContentLabel())
		if !found || field.Value != test.text || !field.ReadOnly {
			t.Fatalf("%s preview = %+v", test.label, field)
		}
		if _, found := tester.Access(locales.FileVersion()); found {
			t.Fatal("preview has an independent version selector")
		}
	}
}

func TestNativeBrowserEmptyAndNoFilterMatchStates(t *testing.T) {
	app, _, finish := nativeFixture(t)
	app.switchWorkspace("files")
	finish()
	app.browser.query = "no-such-path"
	tester := ui.NewTester(app.browserWorkspace, 1024, 680)
	if _, found := tester.Find(locales.NoMatchingFiles()); !found {
		t.Fatal("filtered browser omitted the empty-result state")
	}
	app.browser = newNativeBrowser()
	tester.Frame()
	if _, found := tester.Find(locales.EmptyRepositoryFiles()); !found {
		t.Fatal("empty repository browser omitted the empty state")
	}
}

func TestNativeBrowserCanceledSelectionAndRepositoryReset(t *testing.T) {
	app, path, finish := nativeFixture(t)
	writeNativeFile(t, path, "next.txt", "next\n")
	app.switchWorkspace("files")
	finish()
	app.selectBrowserFile("hello.txt")
	finish()
	buffer := app.browser.viewer.state(1).Buffer
	app.selectBrowserFile("next.txt")
	app.cancelRead()
	finish()
	if app.browser.file != "hello.txt" || app.browser.viewer.state(1).Buffer != buffer {
		t.Fatal("canceling a selection changed successful browser content")
	}
	other, _, _ := nativeFixture(t)
	app.adopt(other.repository)
	if app.browser != nil {
		t.Fatal("switching repository retained another repository's browser state")
	}
}

func TestNativeDiffBrowserContextUsesDeletedAndAddedRows(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.setDiff(gitrepo.Diff{
		Text: "diff --git a/hello.txt b/hello.txt\n--- a/hello.txt\n+++ b/hello.txt\n@@ -2 +2 @@\n-old\n+new\n",
		Old:  &gitrepo.FileContent{Path: "hello.txt", Revision: "HEAD", Present: true, Text: "one\nold\nthree\n"},
		New:  &gitrepo.FileContent{Path: "hello.txt", Revision: "WORKTREE", Present: true, Text: "one\nnew\nthree\n"},
	})
	app.prefs.DiffLayout = "unified"
	ui.NewTester(app.fileDiffPanel, 800, 500)
	unified := &app.unifiedDiffs[0]
	for index, row := range unified.rows {
		if row.kind != '-' && row.kind != '+' {
			continue
		}
		unified.state.GoTo(editor.Pos{Line: index})
		source, position := app.diffBrowserContext()
		want := "WORKTREE"
		if row.kind == '-' {
			want = "HEAD"
		}
		if source == nil || source.Revision != want || position.Line != 1 {
			t.Fatalf("row %c = %+v, %+v", row.kind, source, position)
		}
	}
}

func TestNativeDiffBrowseJumpKeepsExactRevisionAndLine(t *testing.T) {
	app, path, finish := nativeFixture(t)
	writeNativeFile(t, path, "hello.txt", "first\nindex\nthird\n")
	nativeGit(t, path, "add", ".")
	writeNativeFile(t, path, "hello.txt", "first\nworktree\nthird\n")
	app.refresh()
	finish()
	app.selectFile("hello.txt", false)
	finish()
	app.fileViewer.full, app.fileViewer.side = true, 0
	app.fileViewer.state(0).GoTo(editor.Pos{Line: 1, Col: 2})
	app.browseDiffFile()
	finish()
	source := app.browser.viewer.sources[app.browser.viewer.side]
	position := app.browser.viewer.state(app.browser.viewer.side).CursorPos()
	if app.screen != "files" || source.Revision != "INDEX" || source.Text != "first\nindex\nthird\n" ||
		position.Line != 1 || position.Col != 2 {
		t.Fatalf("context jump = %+v, %+v", source, position)
	}
}

func TestNativeDiffBrowserButtonStaysInsideCompactHeader(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.selectedFile = strings.Repeat("很长的目录/", 20) + "preview.go"
	app.setDiff(gitrepo.Diff{
		Old: &gitrepo.FileContent{Path: app.selectedFile, Revision: "HEAD", Present: true},
		New: &gitrepo.FileContent{Path: app.selectedFile, Revision: "WORKTREE", Present: true},
	})
	for _, locale := range []string{"zh-Hans", "en"} {
		t.Setenv("MYGO_LOCALE", locale)
		for _, width := range []int{360, 1024} {
			for _, dark := range []bool{false, true} {
				tester := ui.NewTester(app.fileDiffPanel, width, 400)
				tester.SetLocale(locale)
				tester.SetDark(dark)
				layout, found := tester.Access(locales.DiffLayout())
				if !found || layout.Offscreen {
					t.Fatal("compact header omitted diff layout")
				}
				button, found := tester.AccessByRole(ui.RoleButton, locales.BrowseFile())
				if !found || button.Disabled || button.Offscreen ||
					button.Bounds.X < layout.Bounds.X+layout.Bounds.W ||
					button.Bounds.X+button.Bounds.W > float32(width) {
					t.Fatalf("%s width=%d: line-context button clipped or unavailable: %+v", locale, width, button)
				}
				controls := []string{locales.BrowseFile(), locales.FileDiff(), locales.FullFile()}
				for _, label := range controls {
					control, found := tester.Access(label)
					if !found || control.Offscreen || control.Bounds.X < 0 ||
						control.Bounds.X+control.Bounds.W > float32(width) {
						t.Fatalf("%s width=%d: %s clipped: %+v", locale, width, label, control)
					}
					center := control.Bounds.Y + control.Bounds.H/2
					if center < layout.Bounds.Y || center > layout.Bounds.Y+layout.Bounds.H {
						t.Fatalf("%s width=%d: %s left the compact header row", locale, width, label)
					}
				}
				if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
					saveNativeScreenshot(t, tester, directory,
						fmt.Sprintf("browser-diff-header-%s-%d-dark-%t.png", locale, width, dark))
				}
			}
		}
	}
}

func TestNativeBrowserScreenshots(t *testing.T) {
	directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS")
	if directory == "" {
		t.Skip("未请求界面截图")
	}
	app, path, finish := nativeFixture(t)
	file := "internal/preview/" + strings.Repeat("long_file_name_", 5) + "reader.go"
	if err := os.MkdirAll(filepath.Join(path, filepath.Dir(file)), 0755); err != nil {
		t.Fatal(err)
	}
	text := "package preview\n\nimport \"strings\"\n\nfunc ReadContext(text string) []string {\n\treturn strings.Split(text, \"\\n\")\n}\n"
	writeNativeFile(t, path, file, text)
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "browser screenshot")
	app.switchWorkspace("files")
	finish()
	app.selectBrowserFile(file)
	finish()
	for _, dark := range []bool{false, true} {
		for _, width := range []int{1024, 1440} {
			tester := ui.NewTester(app.view, width, 680)
			tester.SetDark(dark)
			field, found := tester.AccessByRole(ui.RoleTextField, locales.FileContentLabel())
			if !found || field.Offscreen || !field.ReadOnly || field.Value != text ||
				field.Bounds.X < 0 || field.Bounds.X+field.Bounds.W > float32(width) {
				t.Fatalf("browser content missing or clipped at width=%d: %+v", width, field)
			}
			for _, label := range []string{locales.FileHistory(), locales.FindInFile()} {
				control, found := tester.Access(label)
				if !found || control.Offscreen || control.Bounds.X+control.Bounds.W > float32(width) {
					t.Fatalf("browser control clipped at width=%d: %s, %+v", width, label, control)
				}
			}
			saveNativeScreenshot(t, tester, directory, fmt.Sprintf("browser-files-%d-dark-%t.png", width, dark))
			app.browser.query = "no-such-path"
			tester.Frame()
			saveNativeScreenshot(t, tester, directory, fmt.Sprintf("browser-no-matches-%d-dark-%t.png", width, dark))
			app.browser.query = ""
		}
	}
	app.browser = newNativeBrowser()
	tester := ui.NewTester(app.view, 1024, 680)
	saveNativeScreenshot(t, tester, directory, "browser-empty-1024.png")
}
