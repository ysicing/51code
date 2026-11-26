// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"image/color"
	"os"
	"testing"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestImageDiffSharedZoomAndPan(t *testing.T) {
	app, _, finish := nativeFixture(t)
	source := func(revision, fill string) *gitrepo.FileContent {
		return &gitrepo.FileContent{Path: "art.svg", Revision: revision, Present: true, ImageFormat: "svg",
			Text: `<svg xmlns="http://www.w3.org/2000/svg" width="32" height="24"><rect width="32" height="24" fill="` + fill + `"/></svg>`}
	}
	old, next := source("INDEX", "#ff0000"), source("WORKTREE", "#0000ff")
	app.setDiff(gitrepo.Diff{Old: old, New: next})
	handled := false
	tester := ui.NewTester(func(c *ui.Context) {
		handled = app.imageDiffPanel(c)
	}, 440, 260)
	finish()
	tester.Frame()
	sides := [2]*gitrepo.FileContent{old, next}
	colors := [2]color.RGBA{{R: 255, A: 255}, {B: 255, A: 255}}
	bounds := func(index int) ui.Rect {
		t.Helper()
		side := sides[index]
		node, found := tester.AccessByRole(ui.RoleImage, fileVersionLabel(side)+" · "+side.Path)
		if !found || node.Offscreen || node.Bounds.W < 100 || node.Bounds.H < 100 {
			t.Fatalf("side %s has no usable image viewport: found=%t, offscreen=%t, bounds=%+v",
				side.Revision, found, node.Offscreen, node.Bounds)
		}
		return node.Bounds
	}
	assertColor := func(dx, dy int, inside bool) {
		t.Helper()
		frame := tester.Image()
		for index := range sides {
			area := bounds(index)
			x, y := int(area.X+area.W/2)+dx, int(area.Y+area.H/2)+dy
			if got := frame.RGBAAt(x, y); (got == colors[index]) != inside {
				t.Fatalf("side %s at offset (%d,%d), pixel (%d,%d): color %v, expected inside=%v",
					sides[index].Revision, dx, dy, x, y, got, inside)
			}
		}
	}
	click := func(label string) {
		t.Helper()
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	drag := func(side, dx, dy int) {
		area := bounds(side)
		x, y := area.X+area.W/2, area.Y+area.H/2
		tester.Press(x, y)
		tester.Move(x+float32(dx), y+float32(dy))
		tester.Release(x+float32(dx), y+float32(dy))
	}
	assertColor(0, 0, true)
	assertColor(24, 0, false)
	click(locales.ImageDiffZoomIn())
	assertColor(24, 0, true)
	click(locales.ImageDiffZoomOut())
	assertColor(24, 0, false)
	click(locales.ImageDiffZoomIn())
	drag(1, 48, 4)
	assertColor(48, 4, true)
	assertColor(0, 0, false)
	drag(0, -48, -4)
	assertColor(0, 0, true)
	drag(0, 20, 0)
	click(locales.ImageFitToView())
	assertColor(0, 0, true)
	assertColor(24, 0, false)
	click(locales.DiffText())
	if handled {
		t.Fatal("text mode did not return control to the patch viewer")
	}
	click(locales.ViewImage())
	if !handled {
		t.Fatal("image mode did not resume")
	}
	directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS")
	if directory != "" {
		saveNativeScreenshot(t, tester, directory, "image-diff-light.png")
	}
	tester.SetDark(true)
	assertColor(0, 0, true)
	if directory != "" {
		saveNativeScreenshot(t, tester, directory, "image-diff-dark.png")
	}
}

func TestImageDiffSourceChangeDiscardsPendingDecode(t *testing.T) {
	updates := make(chan func(), 2)
	app := &nativeApp{ctx: t.Context(), prefs: &nativePreferences{}, post: func(update func()) { updates <- update }}
	pending := func() func() {
		t.Helper()
		select {
		case update := <-updates:
			return update
		case <-time.After(5 * time.Second):
			t.Fatal("image decoder did not post a result")
			return nil
		}
	}
	set := func(side *gitrepo.FileContent) {
		app.setDiff(gitrepo.Diff{
			Old: &gitrepo.FileContent{Path: side.Path, Revision: "INDEX"},
			New: side,
		})
	}
	set(&gitrepo.FileContent{Path: "bad.png", Revision: "WORKTREE", Present: true, ImageFormat: "png", ImageData: "broken"})
	tester := ui.NewTester(func(c *ui.Context) { app.imageDiffPanel(c) }, 380, 240)
	first := pending()
	set(&gitrepo.FileContent{Path: "large.png", Revision: "WORKTREE", Present: true, ImageFormat: "png", Truncated: true})
	tester.Frame()
	second := pending()
	second()
	first()
	tester.Frame()
	for _, message := range []string{locales.AbsentVersion(), locales.ImagePreviewTooLarge()} {
		if _, found := tester.Find(message); !found {
			t.Fatalf("missing current side status %q", message)
		}
	}
	if _, found := tester.Find(locales.ImagePreviewUnavailable()); found {
		t.Fatal("previous selection replaced the current images")
	}
}

func TestImageDiffAlignsDifferentDimensions(t *testing.T) {
	state := nativeImageDiff{width: 800, height: 400, zoom: 1}
	area := ui.Rect{W: 416, H: 216}
	old := state.imageBox(area, 800, 200)
	next := state.imageBox(area, 400, 400)
	if old.X != next.X || old.Y != next.Y || old.W != 400 || next.W != 200 || old.H != 100 || next.H != 200 {
		t.Fatalf("mismatched coordinate systems: %+v / %+v", old, next)
	}
}
