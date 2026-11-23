// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestNativeRemoteProgressStatusAndFailureDetails(t *testing.T) {
	app, _, finish := nativeFixture(t)
	tester := ui.NewTester(app.view, 900, 650)
	release := make(chan error, 1)
	operation := func(ctx context.Context, _ string) error {
		select {
		case err := <-release:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	app.runRemoteSync(nil, "push", locales.Pushing(), "origin/main", operation)
	app.remoteProgress.progress = gitrepo.RemoteProgress{Phase: gitrepo.RemoteSending, Percent: 68}
	tester.Frame()
	if _, found := tester.Find("68%"); !found {
		t.Fatal("status bar did not show upload progress")
	}
	if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
		saveNativeScreenshot(t, tester, directory, "remote-progress.png")
	}
	app.prefs.Appearance = appearanceDark
	app.remoteProgress.target += strings.Repeat("/long-branch", 8)
	tester.SetSize(780, 480)
	tester.Frame()
	if bounds, found := tester.Find("68%"); !found || bounds.X+bounds.W > 780 {
		t.Fatalf("progress is clipped in a compact dark window: %+v", bounds)
	}
	release <- errors.New("remote rejected update")
	finish()
	tester.Frame()
	if app.dialog != nil || app.problem != "remote rejected update" {
		t.Fatalf("failure should be visible without opening a dialog: %q", app.problem)
	}
	if err := tester.Click(locales.ShowDetails()); err != nil {
		t.Fatal(err)
	}
	if app.dialog == nil || app.dialog.errorDetails == nil {
		t.Fatal("could not open failure details")
	}
	app.closeDialog(app.dialog)
	app.runRemoteSync(nil, "pull", locales.Pulling(), "origin/main", operation)
	release <- nil
	finish() // 刷新阶段
	finish() // 操作结束
	tester.Frame()
	if _, found := tester.Find(locales.RemotePullComplete()); !found {
		t.Fatal("status bar did not show the completed pull")
	}
}
