// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func testTerminalApplication(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Terminal")
	if runtime.GOOS == "darwin" {
		path += ".app"
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	} else {
		if runtime.GOOS == "windows" {
			path += ".exe"
		}
		if err := os.WriteFile(path, nil, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestNativeTerminalDiscoveryDeduplication(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "iTerm.app")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.app")
	if err := os.Symlink(path, alias); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, errors.ErrUnsupported) ||
			runtime.GOOS == "windows" && (errors.Is(err, syscall.Errno(1314)) || errors.Is(err, syscall.Errno(50))) {
			t.Skipf("filesystem or account cannot create symlinks: %v", err)
		}
		t.Fatal(err)
	}
	applications := macTerminalApplications([]mygo.InstalledApp{
		{Name: "Terminal", ID: "com.apple.Terminal", Path: "/System/Applications/Utilities/Terminal.app"},
		{Name: "iTerm2", ID: "com.googlecode.iterm2", Path: path},
		{Name: "iTerm", ID: "com.googlecode.iterm2", Path: alias},
		{Name: "Alias", ID: "co.zeit.hyper", Path: alias},
		{Name: "Editor", ID: "com.example.editor", Path: "/Applications/Editor.app"},
		{Name: "Ghostty", ID: "com.mitchellh.ghostty", Path: "/Applications/Ghostty.app"},
	})
	if len(applications) != 2 || applications[0].name != "Ghostty" ||
		applications[1].name != "iTerm2" || applications[1].path != path {
		t.Fatalf("unexpected discovered terminals: %+v", applications)
	}
}

func TestNativeTerminalDiscoveryMenu(t *testing.T) {
	app, _, finish := nativeFixture(t)
	application := testTerminalApplication(t)
	app.discoverTerminals = func() ([]nativeTerminalApplication, error) {
		return []nativeTerminalApplication{{"Installed Terminal", application}}, nil
	}
	tester := sidebarSettingsTester(t, app)
	finish()
	tester.Frame()
	button := nativeSettingsControl(t, tester, ui.RoleMenuButton, "终端")
	tester.Press(button.Bounds.X+button.Bounds.W/2, button.Bounds.Y+button.Bounds.H/2)
	tester.Release(button.Bounds.X+button.Bounds.W/2, button.Bounds.Y+button.Bounds.H/2)
	if err := tester.ChooseMenuItem("Installed Terminal"); err != nil {
		t.Fatal(err)
	}
	if app.prefs.TerminalApplication != application || app.terminalApplicationLabel() != "Installed Terminal" {
		t.Fatal("discovered terminal selection did not preserve its path and display name")
	}
	restored := newNativeApp(app.service, app.stateFile)
	t.Cleanup(restored.cancel)
	if restored.prefs.TerminalApplication != application {
		t.Fatal("discovered terminal selection was not persisted")
	}
}

func TestNativeTerminalDiscoveryDiscardsStaleResult(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "replaced", true: "closed"}[closed], func(t *testing.T) {
			app, _, finish := nativeFixture(t)
			started := make(chan struct{})
			release := make(chan struct{})
			app.discoverTerminals = func() ([]nativeTerminalApplication, error) {
				close(started)
				<-release
				return []nativeTerminalApplication{{"Stale", "/stale"}}, nil
			}
			dialog := &nativeDialog{}
			app.dialog = dialog
			app.refreshTerminalApplications(dialog)
			<-started
			app.closed = closed
			if !closed {
				app.dialog = &nativeDialog{}
			}
			close(release)
			finish()
			if len(app.terminalApplications) != 0 {
				t.Fatal("obsolete discovery updated a closed or replaced settings dialog")
			}
		})
	}
}
