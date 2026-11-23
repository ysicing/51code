// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"testing"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
)

func TestNativeSecuritySettingsAndStagedSecretReview(t *testing.T) {
	mygo.App.SetPath(mygo.PathUserData, t.TempDir())
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })
	settingsTestUpdater.Do(func() {
		mygo.Use(native.New(updater.Options{DisableAutomaticChecks: true, Language: "zh-Hans"}))
	})
	app, path, finish := nativeFixture(t)
	if app.prefs.SecretScanEnabled {
		t.Fatal("secret scanning should be opt-in")
	}
	app.settingsDialogAt(5)
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click(locales.EnableSecretScan()); err != nil {
		t.Fatal(err)
	}
	if !app.prefs.SecretScanEnabled {
		t.Fatal("security setting did not enable scanning")
	}
	app.dialog.onClose()
	file := "test.env"
	writeNativeFile(t, path, file, "TOKEN="+"ghp_"+"R7c4Wk2nH3xV8eJ9qP0uL5rD1mS6aT4yN8zB\n")
	nativeGit(t, path, "add", "--", file)
	app.refresh()
	finish()
	tester.Frame()
	if err := tester.Click(locales.ScanStagedSecrets()); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if app.dialog == nil || app.dialog.title != locales.SecretScanTitle() || app.dialog.action != "" {
		t.Fatal("manual scan did not show a read-only report")
	}
	if err := tester.Click(file + ":1"); err != nil {
		t.Fatal(err)
	}
	finish()
	if app.dialog != nil || app.selectedFile != file || !app.staged {
		t.Fatal("scan finding did not open the staged diff")
	}
	app.prefs.Drafts[app.repository.snapshot.Path] = "review sample"
	before := nativeGit(t, path, "rev-parse", "HEAD")
	app.commit()
	finish()
	if app.dialog == nil || !app.dialog.danger || app.dialog.action != locales.ContinueCommit() ||
		nativeGit(t, path, "rev-parse", "HEAD") != before {
		t.Fatal("commit did not wait for secret scan confirmation")
	}
	app.dialog.submit(app.dialog)
	finish()
	if app.problem != "" || nativeGit(t, path, "rev-parse", "HEAD") == before {
		t.Fatal("explicitly approved commit did not complete")
	}
}

func TestNativeHistoryWorkingScansStagedSecrets(t *testing.T) {
	app, path, finish := nativeFixture(t)
	app.prefs.SecretScanEnabled = true
	const filename = "test.env"
	writeNativeFile(t, path, filename, "TOKEN="+"ghp_"+"R7c4Wk2nH3xV8eJ9qP0uL5rD1mS6aT4yN8zB\n")
	nativeGit(t, path, "add", "--", filename)
	writeNativeFile(t, path, filename, "clean unstaged version\n")
	app.refresh()
	finish()
	app.screen = "history"
	app.searchHistory(false)
	finish()
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click(locales.UncommittedFiles("1")); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if !app.historyWorking || app.staged {
		t.Fatal("history should initially show the unstaged version")
	}
	if err := tester.Click(locales.ScanStagedSecrets()); err != nil {
		t.Fatal(err)
	}
	if app.readTask == nil {
		t.Fatal("clicking the history working tree scan did not start a task")
	}
	finish()
	tester.Frame()
	if app.dialog == nil || app.dialog.title != locales.SecretScanTitle() || app.dialog.action != "" {
		t.Fatal("history scan did not show a read-only report")
	}
	if err := tester.Click(filename + ":1"); err != nil {
		t.Fatal(err)
	}
	finish()
	if app.dialog != nil || app.screen != "history" || !app.historyWorking ||
		!app.staged || app.selectedFile != filename {
		t.Fatal("finding did not open the staged diff within the history working tree")
	}
}
