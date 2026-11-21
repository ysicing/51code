// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ysicing/51code/internal/terminal"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestNativeDirectoryPickerMemory(t *testing.T) {
	app, _, _ := nativeFixture(t)
	directory := filepath.Join(t.TempDir(), "中文 repo;$(sentinel)")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	app.openDialog = func(options mygo.OpenDialogOptions) ([]string, error) {
		if !app.busy || !options.Directory || !options.Multiple || options.DefaultPath != userHomeDirectory() {
			t.Fatalf("incorrect initial picker options: %+v", options)
		}
		return []string{directory}, nil
	}
	if _, err := app.chooseDirectories("添加", "添加", true); err != nil || app.busy {
		t.Fatalf("directory selection failed: %v", err)
	}
	restored := newNativeApp(app.service, app.stateFile)
	t.Cleanup(restored.cancel)
	if restored.prefs.LastDirectory != directory || restored.directoryDialogPath() != directory {
		t.Fatal("last directory was not persisted")
	}
	for _, failure := range []error{nil, errors.New("picker failure")} {
		app.openDialog = func(options mygo.OpenDialogOptions) ([]string, error) {
			if options.DefaultPath != directory || options.Multiple {
				t.Fatalf("scan picker did not reuse selected directory: %+v", options)
			}
			return nil, failure
		}
		if _, err := app.chooseDirectories("扫描", "扫描", false); !errors.Is(err, failure) ||
			app.prefs.LastDirectory != directory || app.busy {
			t.Fatal("cancellation or error changed directory memory")
		}
	}
	app.prefs.LastDirectory = filepath.Join(directory, "removed")
	if app.directoryDialogPath() != userHomeDirectory() {
		t.Fatal("missing directory did not fall back to home")
	}
	app.prefs.LastDirectory = filepath.Join(directory, "file")
	if err := os.WriteFile(app.prefs.LastDirectory, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if app.directoryDialogPath() != userHomeDirectory() {
		t.Fatal("regular file was accepted as picker directory")
	}
}

func TestNativeDirectoryPickerSaveFailure(t *testing.T) {
	app, _, _ := nativeFixture(t)
	previous := t.TempDir()
	app.prefs.LastDirectory = previous
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	app.stateFile = filepath.Join(file, "state.json")
	app.openDialog = func(mygo.OpenDialogOptions) ([]string, error) {
		return []string{t.TempDir()}, nil
	}
	if paths, err := app.chooseDirectories("添加", "添加", true); err == nil || len(paths) != 0 ||
		app.prefs.LastDirectory != previous || app.busy {
		t.Fatal("failed preference save did not preserve the previous directory")
	}
}

func TestNativeTerminalPreferences(t *testing.T) {
	app, _, _ := nativeFixture(t)
	tester := sidebarSettingsTester(t, app)
	insights, found := tester.AccessByRole(ui.RoleCheckBox, "洞察")
	search, searchFound := tester.AccessByRole(ui.RoleCheckBox, "搜索")
	if !found || !searchFound || insights.Bounds.X >= search.Bounds.X || insights.Bounds.Y != search.Bounds.Y {
		t.Fatal("sidebar options did not use the chosen two-column layout")
	}
	clickTerminal := func() {
		t.Helper()
		button := nativeSettingsControl(t, tester, ui.RoleMenuButton, "终端")
		tester.Press(button.Bounds.X+button.Bounds.W/2, button.Bounds.Y+button.Bounds.H/2)
		tester.Release(button.Bounds.X+button.Bounds.W/2, button.Bounds.Y+button.Bounds.H/2)
		if tester.Menu() == nil {
			t.Fatalf("terminal selector did not open: %+v", button.Bounds)
		}
	}
	clickTerminal()
	if err := tester.ChooseMenuItem(systemTerminalLabel()); err != nil {
		t.Fatal(err)
	}
	if app.prefs.TerminalApplication != terminal.System || app.settingsError != "" {
		t.Fatal("system terminal selection was not saved", app.settingsError)
	}
	application := filepath.Join(t.TempDir(), "Other Terminal")
	if runtime.GOOS == "darwin" {
		application += ".app"
		if err := os.Mkdir(application, 0700); err != nil {
			t.Fatal(err)
		}
	} else {
		if runtime.GOOS == "windows" {
			application += ".exe"
		}
		if err := os.WriteFile(application, []byte("terminal"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	app.openDialog = func(options mygo.OpenDialogOptions) ([]string, error) {
		if !app.busy || options.Directory || options.Multiple {
			t.Fatalf("terminal picker options incorrect: %+v", options)
		}
		if runtime.GOOS == "darwin" && len(options.Filters) != 0 {
			t.Fatal("macOS application bundles must not use the legacy app extension filter")
		}
		return []string{application}, nil
	}
	clickTerminal()
	if err := tester.ChooseMenuItem("选择其他应用"); err != nil {
		t.Fatal(err)
	}
	restored := newNativeApp(app.service, app.stateFile)
	t.Cleanup(restored.cancel)
	if restored.prefs.TerminalApplication != application || app.settingsError != "" || app.busy {
		t.Fatal("custom terminal selection did not persist", app.settingsError)
	}
	app.openDialog = func(mygo.OpenDialogOptions) ([]string, error) { return nil, nil }
	app.chooseTerminalApplication()
	if app.prefs.TerminalApplication != application {
		t.Fatal("cancelling the application picker lost the selection")
	}
	app.setTerminalApplication(filepath.Join(t.TempDir(), "missing"))
	if app.prefs.TerminalApplication != application || app.settingsError == "" {
		t.Fatal("missing application replaced the existing terminal")
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	app.stateFile = filepath.Join(file, "state.json")
	app.setTerminalApplication("")
	if app.prefs.TerminalApplication != application || app.settingsError == "" {
		t.Fatal("save failure did not roll back the terminal preference")
	}
}

func TestNativeOpenTerminalFailure(t *testing.T) {
	app, _, finish := nativeFixture(t)
	app.screen = "library"
	missing := filepath.Join(t.TempDir(), "missing-terminal")
	app.prefs.TerminalApplication = missing
	tester := ui.NewTester(app.view, 960, 640)
	if err := tester.Click("管理 " + app.library.Repositories[0].Name); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("在终端中打开"); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if !strings.Contains(app.problem, missing) || app.busy || app.screen != "library" ||
		len(app.service.Library().Repositories) != 1 {
		t.Fatal("terminal launch failure was not reported safely", app.problem)
	}
}

func TestNativeRepositoryTerminalMenu(t *testing.T) {
	for _, writable := range []bool{true, false} {
		t.Run(map[bool]string{true: "writable", false: "readonly"}[writable], func(t *testing.T) {
			app, _, finish := nativeFixture(t)
			app.writable = writable
			missing := filepath.Join(t.TempDir(), "selected-terminal-missing")
			app.prefs.TerminalApplication = missing
			tester := ui.NewTester(app.view, 960, 640)
			if err := tester.Click("更多"); err != nil {
				t.Fatal(err)
			}
			if err := tester.ChooseMenuItem("在终端中打开"); err != nil {
				t.Fatal(err)
			}
			finish()
			tester.Frame()
			if !strings.Contains(app.problem, missing) || app.busy || app.dialog == nil ||
				app.dialog.errorDetails == nil || app.dialog.parent != nil {
				t.Fatal("repository terminal action did not use the selected terminal safely", app.problem)
			}
		})
	}
}
