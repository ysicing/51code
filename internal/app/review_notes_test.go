// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func TestReviewSelectionsUseSourceCoordinates(t *testing.T) {
	patches, err := editor.ParsePatch("--- a/old.txt\n+++ b/new.txt\n" +
		"@@ -10,2 +20,2 @@\n context\n-中文\n+```new\n" +
		"@@ -50 +60 @@\n-last old\n+last new\n")
	if err != nil {
		t.Fatal(err)
	}
	diff := nativeUnifiedDiff{patch: patches[0]}
	diff.build("new.txt")
	text := diff.state.Buffer.Snapshot()
	diff.state.Select(text.LineStart(4), text.LineStart(1)+1)
	want := []reviewExcerpt{
		{"old.txt", editor.DiffOld, 10, 11, "context\n中文"},
		{"new.txt", editor.DiffNew, 21, 21, "```new"},
	}
	if got := diff.reviewSelection(false); !slices.Equal(got, want) {
		t.Fatalf("reversed unified selection = %#v, want %#v", got, want)
	}
	diff.state.Select(text.LineStart(4), text.LineStart(5))
	if got := diff.reviewSelection(false); len(got) != 0 {
		t.Fatalf("hunk header became a source line: %#v", got)
	}
	diff.state.Select(text.LineStart(5), text.LineStart(5))
	if got := diff.reviewSelection(true); !slices.Equal(got, []reviewExcerpt{{"old.txt", editor.DiffOld, 50, 50, "last old"}}) {
		t.Fatalf("caret review line = %#v", got)
	}
	var split editor.DiffState
	split.SetPatch(patches[0])
	for _, side := range []editor.DiffSide{editor.DiffOld, editor.DiffNew} {
		got := diff.splitReviewSelection(&split, editor.DiffLineRange{Side: side, Start: 1, End: 2})
		want := []reviewExcerpt{
			{"old.txt", editor.DiffOld, 11, 11, "中文"},
			{"old.txt", editor.DiffOld, 50, 50, "last old"},
		}
		if side == editor.DiffNew {
			want = []reviewExcerpt{
				{"new.txt", editor.DiffNew, 21, 21, "```new"},
				{"new.txt", editor.DiffNew, 60, 60, "last new"},
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("split selection = %#v, want %#v", got, want)
		}
	}
	split.Old = editor.NewBuffer("unchanged\nold\n\nend\n")
	diff.context = split.Old
	got := diff.splitReviewSelection(&split, editor.DiffLineRange{Side: editor.DiffOld, Start: 1, End: 3})
	if len(got) != 1 || got[0] != (reviewExcerpt{"old.txt", editor.DiffOld, 2, 4, "old\n\nend"}) {
		t.Fatalf("full-file selection = %#v", got)
	}
}

func TestReviewNotesSaveCopyAndClear(t *testing.T) {
	app, _, finish := nativeFixture(t)
	repo := app.repository.snapshot.Path
	app.prefs.DiffLayout = "unified"
	app.setDiff(gitrepo.Diff{Text: "--- a/first.go\n+++ b/first.go\n@@ -3,2 +3 @@\n-```\n-  old()\n+new()\n"})
	if len(app.unifiedDiffs) != 1 {
		t.Fatal("expected a structured diff for first.go")
	}
	tester := ui.NewTester(app.view, 1280, 720)
	click := func(label string) {
		t.Helper()
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	menu := func(label string) {
		t.Helper()
		click(locales.ReviewNotes())
		if err := tester.ChooseMenuItem(label); err != nil {
			t.Fatal(err)
		}
	}
	state := &app.unifiedDiffs[0].state
	text := state.Buffer.Snapshot()
	state.Select(text.LineStart(1), text.LineStart(3))
	field, ok := tester.AccessByRole(ui.RoleTextField, locales.DiffText())
	if !ok {
		t.Fatal("missing unified editor")
	}
	tester.RightClickAt(field.Bounds.X+10, field.Bounds.Y+float32(state.LineTop(1))+6)
	if err := tester.ChooseMenuItem(locales.AddReviewNote()); err != nil {
		t.Fatal(err)
	}
	tester.Type("first comment")
	click(locales.Cancel())
	click(locales.Cancel())
	click(locales.Save())
	prefix := strings.Repeat("unchanged\n", 7)
	second := gitrepo.Diff{
		Text: "--- a/second.go\n+++ b/second.go\n@@ -8 +8 @@\n-old()\n+new()\n",
		Old:  &gitrepo.FileContent{Path: "second.go", Present: true, Text: prefix + "old()\n"},
		New:  &gitrepo.FileContent{Path: "second.go", Present: true, Text: prefix + "new()\n"},
	}
	showSplit := func() {
		t.Helper()
		app.prefs.DiffLayout = "split"
		app.setDiff(second)
		if len(app.diffViews) != 1 || app.unifiedDiffs[0].context == nil {
			t.Fatal("expected a diff with full file versions for second.go")
		}
		tester.Frame()
	}
	selectEndingAtPlaceholder := func(start int) {
		app.diffViews[0].SetSelectedLines(editor.DiffLineRange{Side: editor.DiffNew, Start: start, End: 8}, true)
		tester.Frame()
	}
	showSplit()
	selectEndingAtPlaceholder(8)
	click(locales.ReviewNotes())
	if err := tester.ChooseMenuItem(locales.AddReviewNote()); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected adding a note on the placeholder alone to be disabled, got %v", err)
	}
	tester.CloseMenu()
	selectEndingAtPlaceholder(7)
	menu(locales.AddReviewNote())
	tester.Type("second comment")
	click(locales.Save())
	menu(locales.CopyReviewNotesMarkdown())
	copied := tester.Clipboard()
	for _, want := range []string{"first.go:L3-L4", locales.ReviewNoteOldSide(), "````\n```\n  old()\n````",
		"first comment", "second.go:L8 (", locales.ReviewNoteNewSide(), "```\nnew()\n```", "second comment"} {
		if !strings.Contains(copied, want) {
			t.Fatalf("clipboard missing %q: %q", want, copied)
		}
	}
	otherPath := t.TempDir()
	nativeGit(t, otherPath, "init", "-b", "main")
	app.openRepository(otherPath)
	finish()
	otherRepo := app.repository.snapshot.Path
	if app.problem != "" || otherRepo == repo {
		t.Fatalf("could not switch repositories: %s", app.problem)
	}
	showSplit()
	selectEndingAtPlaceholder(7)
	menu(locales.AddReviewNote())
	tester.Type("private to B")
	click(locales.Save())
	menu(locales.ClearReviewNotes())
	click(locales.Cancel())
	if len(app.reviewNotes[otherRepo]) != 1 {
		t.Fatal("canceling clear removed notes")
	}
	menu(locales.ClearReviewNotes())
	confirm, found := tester.AccessByRole(ui.RoleButton, locales.ClearReviewNotes())
	if !found || confirm.Disabled || confirm.Offscreen {
		t.Fatal("missing enabled clear confirmation button")
	}
	tester.ClickAt(confirm.Bounds.X+confirm.Bounds.W/2, confirm.Bounds.Y+confirm.Bounds.H/2)
	if remaining := len(app.reviewNotes[otherRepo]); remaining != 0 {
		t.Fatalf("cleared repository still has %d notes", remaining)
	}
	if got := app.reviewNotes.markdown(repo); got != copied {
		t.Fatalf("clearing the other repository changed Markdown: got %q, want %q", got, copied)
	}
}

func TestReviewNotesKeepGitRenamePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 文件名不支持制表符或末尾空格")
	}
	for _, test := range []struct {
		name, oldPath, newPath string
		quoted                 bool
	}{
		{"quoted", "旧 名\told.go", "新 名\tnew.go", true},
		{"trailing-space", "old name.go ", "new name.go ", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, path, _ := nativeFixture(t)
			nativeGit(t, path, "config", "core.quotePath", "true")
			const unchangedLines = 12
			context := strings.Repeat("line kept for rename similarity\n", unchangedLines)
			writeNativeFile(t, path, test.oldPath, context+"old\n")
			nativeGit(t, path, "add", "--", test.oldPath)
			nativeGit(t, path, "commit", "-m", "before rename")
			nativeGit(t, path, "mv", "--", test.oldPath, test.newPath)
			writeNativeFile(t, path, test.newPath, context+"new\n")
			nativeGit(t, path, "add", "--", test.newPath)

			repo := app.repository.snapshot.Path
			preview, err := app.service.Diff(app.ctx, repo, test.newPath, true)
			if err != nil {
				t.Fatal(err)
			}
			app.setDiff(preview)
			if len(app.unifiedDiffs) != 1 || app.unifiedDiffs[0].context == nil {
				t.Fatal("expected a rename diff with two full file versions")
			}
			diff := &app.unifiedDiffs[0]
			if test.quoted && (diff.patch.OldName != test.oldPath || diff.patch.NewName != test.newPath) {
				t.Fatalf("MyGo did not decode the Git-quoted paths: %q → %q", diff.patch.OldName, diff.patch.NewName)
			}
			var excerpts []reviewExcerpt
			for _, side := range []editor.DiffSide{editor.DiffOld, editor.DiffNew} {
				lines := editor.DiffLineRange{Side: side, Start: unchangedLines, End: unchangedLines}
				excerpts = append(excerpts, diff.splitReviewSelection(app.diffViews[0], lines)...)
			}
			app.addReviewNote(excerpts)
			tester := ui.NewTester(app.view, 1280, 720)
			tester.Type("rename review")
			if err := tester.Click(locales.Save()); err != nil {
				t.Fatal(err)
			}
			notes := app.reviewNotes[repo]
			if len(notes) != 1 || len(notes[0].excerpts) != 2 {
				t.Fatalf("expected one note on both sides: %#v", notes)
			}
			markdown := app.reviewNotes.markdown(repo)
			for index, want := range []string{test.oldPath, test.newPath} {
				excerpt := notes[0].excerpts[index]
				if excerpt.path != want {
					t.Fatalf("saved path = %q, want %q", excerpt.path, want)
				}
				location := excerpt.location()
				if test.quoted {
					location = strconv.Quote(location)
				}
				if !strings.Contains(markdown, location) {
					t.Fatalf("export missing location %q: %q", location, markdown)
				}
			}
		})
	}
}
