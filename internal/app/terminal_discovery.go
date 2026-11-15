// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/egoist/mygo"
)

type nativeTerminalApplication struct {
	name string
	path string
}

func discoverTerminalApplications() ([]nativeTerminalApplication, error) {
	if runtime.GOOS == "darwin" {
		applications, err := mygo.Shell.InstalledApps()
		if err != nil {
			return nil, err
		}
		return macTerminalApplications(applications), nil
	}
	candidates := []nativeTerminalApplication{
		{"GNOME Terminal", "gnome-terminal"}, {"Console", "kgx"},
		{"Konsole", "konsole"}, {"Xfce Terminal", "xfce4-terminal"},
		{"Tilix", "tilix"}, {"Alacritty", "alacritty"}, {"kitty", "kitty"},
	}
	if runtime.GOOS == "windows" {
		candidates = []nativeTerminalApplication{
			{"Windows Terminal", "wt.exe"}, {"PowerShell", "pwsh.exe"},
			{"Windows PowerShell", "powershell.exe"},
		}
	}
	var found []nativeTerminalApplication
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate.path); err == nil {
			found = append(found, nativeTerminalApplication{candidate.name, path})
		}
	}
	return uniqueTerminalApplications(found), nil
}

func macTerminalApplications(applications []mygo.InstalledApp) []nativeTerminalApplication {
	known := map[string]bool{
		"com.googlecode.iterm2": true, "net.kovidgoyal.kitty": true,
		"org.alacritty": true, "com.mitchellh.ghostty": true,
		"dev.warp.warp-stable": true, "com.github.wez.wezterm": true,
		"co.zeit.hyper": true, "org.tabby": true,
	}
	var found []nativeTerminalApplication
	identities := make(map[string]bool)
	for _, application := range applications {
		identity := strings.ToLower(application.ID)
		if !known[identity] || identities[identity] || application.Path == "" {
			continue
		}
		identities[identity] = true
		found = append(found, nativeTerminalApplication{application.Name, application.Path})
	}
	return uniqueTerminalApplications(found)
}

func terminalApplicationPath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func uniqueTerminalApplications(applications []nativeTerminalApplication) []nativeTerminalApplication {
	found := make([]nativeTerminalApplication, 0, len(applications))
	paths := make(map[string]bool)
	for _, application := range applications {
		resolved := application.path
		if path, err := filepath.EvalSymlinks(resolved); err == nil {
			resolved = path
		}
		path := terminalApplicationPath(resolved)
		if !paths[path] {
			paths[path] = true
			found = append(found, application)
		}
	}
	slices.SortStableFunc(found, func(left, right nativeTerminalApplication) int {
		return strings.Compare(strings.ToLower(left.name), strings.ToLower(right.name))
	})
	return found
}

func (app *nativeApp) refreshTerminalApplications(dialog *nativeDialog) {
	discover := app.discoverTerminals
	if discover == nil {
		return
	}
	go func() {
		applications, err := discover()
		if err != nil {
			return
		}
		app.post(func() {
			if !app.closed && app.dialog == dialog {
				app.terminalApplications = applications
			}
		})
	}()
}
