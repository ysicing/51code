// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func TestNativeResolveIndividualConflictBlocks(t *testing.T) {
	app, path, finish := nativeFixture(t)
	fixture := func(first, last string) string {
		return "header\n" + first + "\n" + strings.Repeat("unchanged\n", 15) + last + "\nfooter\n"
	}
	writeNativeFile(t, path, "hello.txt", fixture("base A", "base B"))
	writeNativeFile(t, path, ".gitattributes", "hello.txt conflict-marker-size=4\n")
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "base")
	nativeGit(t, path, "switch", "-c", "incoming")
	writeNativeFile(t, path, "hello.txt", fixture("incoming A", "incoming B"))
	nativeGit(t, path, "commit", "-am", "incoming")
	nativeGit(t, path, "switch", "main")
	writeNativeFile(t, path, "hello.txt", fixture("current A", "current B"))
	nativeGit(t, path, "commit", "-am", "current")
	nativeGit(t, path, "config", "merge.conflictStyle", "diff3")
	if err := exec.Command("git", "-C", path, "merge", "incoming").Run(); err == nil {
		t.Fatal("expected merge conflicts")
	}
	file := filepath.Join(path, "hello.txt")
	original, err := os.ReadFile(file)
	if err != nil || strings.Count(string(original), "<<<< HEAD\n") != 2 {
		t.Fatalf("expected two conflict blocks: %s, %v", original, err)
	}
	app.refresh()
	finish()
	app.selectFile("hello.txt", false)
	finish()
	tester := ui.NewTester(app.view, 1024, 680)
	click := func(label string) {
		t.Helper()
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
	}

	for _, appearance := range []struct {
		dark bool
		file string
	}{
		{false, "conflict-blocks-light.png"},
		{true, "conflict-blocks-dark.png"},
	} {
		tester.SetDark(appearance.dark)
		for _, label := range []string{locales.PreviousHunk(), locales.NextHunk(), locales.AcceptCurrentBlock(), locales.AcceptIncomingBlock(),
			locales.UndoConflictBlockChoice(), locales.SaveAndStage()} {
			button, found := tester.AccessByRole(ui.RoleButton, label)
			if !found || button.Offscreen || button.Bounds.W == 0 || button.Bounds.X+button.Bounds.W > 1024 {
				t.Fatalf("missing or clipped conflict action %q: %+v", label, button)
			}
		}
		if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
			saveNativeScreenshot(t, tester, directory, appearance.file)
		}
	}

	click(locales.AcceptCurrentBlock())
	click(locales.NextHunk())
	if line := app.conflictEdit.CursorPos().Line; line != 17 {
		t.Fatalf("next block cursor line = %d", line)
	}
	click(locales.AcceptIncomingBlock())
	click(locales.PreviousHunk())
	click(locales.UndoConflictBlockChoice())
	draft := app.conflictEdit.Buffer.String()
	if strings.Count(draft, "<<<< HEAD\n") != 1 || !strings.Contains(draft, "incoming B\nfooter") || !strings.Contains(draft, "|||| ") {
		t.Fatalf("undo must restore only the first diff3 block: %q", draft)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != string(original) ||
		nativeGit(t, path, "ls-files", "-u", "--", "hello.txt") == "" {
		t.Fatal("block controls must only modify the draft")
	}
	click(locales.SaveAndStage())
	finish()
	if app.dialog == nil || app.problem == "" || app.conflictEdit.Buffer.String() != draft ||
		nativeGit(t, path, "ls-files", "-u", "--", "hello.txt") == "" {
		t.Fatal("custom conflict markers must block staging and preserve the draft")
	}
	app.closeDialog(app.dialog)
	tester.Frame()
	click(locales.AcceptIncomingBlock())
	click(locales.SaveAndStage())
	finish()
	if got := nativeGit(t, path, "show", ":hello.txt"); got != strings.TrimSpace(fixture("incoming A", "incoming B")) {
		t.Fatalf("staged result = %q, problem = %s", got, app.problem)
	}
}

func TestConflictBlockUndoPreservesManualEdits(t *testing.T) {
	const first = "<<<<<<< HEAD\n=======\nincoming A\n>>>>>>> topic\n"
	const second = "<<<<<<< HEAD\ncurrent B\n=======\nincoming B\n>>>>>>> topic\n"
	buffer := editor.NewBuffer("header\n" + first + second + "footer\n")
	var state nativeConflictBlocks
	state.sync(buffer, 7)
	state.choose(false)
	state.index = 1
	state.choose(true)
	buffer.Edit(editor.Edit{Start: 0, End: len("header"), Text: "edited header"})
	state.sync(buffer, 7)
	if len(state.blocks) != 2 || state.blocks[0].original == "" {
		t.Fatalf("an edit before the block lost its undo record: %+v", state.blocks)
	}
	state.index = 0
	state.undo()
	if got := buffer.String(); got != "edited header\n"+first+"incoming B\nfooter\n" {
		t.Fatalf("undo changed text outside the block: %q", got)
	}
	start := strings.Index(buffer.String(), "incoming B")
	buffer.Edit(editor.Edit{Start: start, End: start + len("incoming B"), Text: "manual B"})
	state.sync(buffer, 7)
	if len(state.blocks) != 1 || state.blocks[0].original != "" {
		t.Fatalf("undo must not replace a manually edited block: %+v", state.blocks)
	}
}

func TestConflictBlocksSkipAmbiguousMarkers(t *testing.T) {
	const nested = "<<<<<<< HEAD\n<<<<<<< other\none\n=======\ntwo\n>>>>>>> incoming\n=======\nthree\n>>>>>>> topic\n"
	const complete = "<<<<<<< HEAD\ncurrent\n=======\n>>>>>>> topic"
	buffer := editor.NewBuffer(nested + complete)
	var state nativeConflictBlocks
	state.sync(buffer, 7)
	if len(state.blocks) != 1 || state.blocks[0].start != len(nested) {
		t.Fatalf("expected only the unambiguous block: %+v", state.blocks)
	}
	state.choose(true)
	if got := buffer.String(); got != nested {
		t.Fatalf("empty choice at EOF should remove only its block: %q", got)
	}
	state.undo()
	if got := buffer.String(); got != nested+complete {
		t.Fatalf("undo should restore original markers at EOF: %q", got)
	}
}

func TestConflictBlockChoicesPreserveMixedLineEndings(t *testing.T) {
	const prefix = "header\r\n"
	const first = "<<<<<<< HEAD\none\r\n=======\ntwo\r\n>>>>>>> topic\n"
	const second = "<<<<<<< HEAD\r\ncurrent\r\n=======\r\nincoming\n>>>>>>> topic\r\n"
	const suffix = "footer"
	for _, test := range []struct {
		name     string
		incoming bool
		chosen   string
	}{
		{"current", false, "one\r\n"},
		{"incoming", true, "two\r\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var app nativeApp
			app.conflictEdit.Buffer = editor.NewBuffer(prefix + first + second + suffix)
			state := &app.conflictBlocks
			state.sync(app.conflictEdit.Buffer, 7)
			state.choose(test.incoming)
			if got := app.conflictDraft(); got != prefix+test.chosen+second+suffix {
				t.Fatalf("first choice = %q", got)
			}
			state.index = 1
			state.choose(true)
			state.index = 0
			state.undo()
			if got := app.conflictDraft(); got != prefix+first+"incoming\n"+suffix {
				t.Fatalf("undo changed the first block's endings or the second choice: %q", got)
			}
			state.index = 1
			state.undo()
			if got := app.conflictDraft(); got != prefix+first+second+suffix {
				t.Fatalf("undo did not restore the original bytes: %q", got)
			}
		})
	}
}
