// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package terminal

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTerminalCommandPreservesDirectoryArguments(t *testing.T) {
	directory := "/projects/中文 repo;$(touch sentinel)"
	lookup := func(name string) (string, error) { return "/bin/" + name, nil }
	for _, test := range []struct {
		platform    string
		application string
		want        []string
	}{
		{"darwin", "", []string{"/usr/bin/open", "-a", "Terminal", "--", directory}},
		{"darwin", "/Applications/Other Terminal.app", []string{"/usr/bin/open", "-a", "/Applications/Other Terminal.app", "--", directory}},
		{"windows", "", []string{"/bin/cmd.exe"}},
		{"windows", "C:/Apps/wt.exe", []string{"C:/Apps/wt.exe", "-d", strings.ReplaceAll(directory, ";", `\;`)}},
		{"linux", "", []string{"/bin/xdg-terminal-exec", "--dir=" + directory}},
		{"linux", "/usr/bin/gnome-terminal", []string{"/usr/bin/gnome-terminal", "--working-directory=" + directory}},
	} {
		t.Run(test.platform+"/"+test.application, func(t *testing.T) {
			command, err := commandFor(test.platform, test.application, directory, lookup)
			if err != nil || !slices.Equal(command.Args, test.want) || command.Dir != directory {
				t.Fatalf("unexpected command: %+v, %v", command, err)
			}
		})
	}
}

func TestWindowsTerminalEscapesCommandSeparators(t *testing.T) {
	directory := `C:\中文 repo;new-tab;\;directory`
	command, err := commandFor("windows", `C:\Apps\WT.EXE`, directory, nil)
	if err != nil || command.Dir != directory ||
		!slices.Equal(command.Args, []string{`C:\Apps\WT.EXE`, "-d", `C:\中文 repo\;new-tab\;\\;directory`}) {
		t.Fatalf("Windows Terminal directory was not escaped: %+v, %v", command, err)
	}
}

func TestTerminalLinuxFallbackKeepsPathOutOfScript(t *testing.T) {
	directory := "/projects/中文 repo;$(touch sentinel)"
	command, err := commandFor("linux", "", directory, func(name string) (string, error) {
		if name == "x-terminal-emulator" {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not installed")
	})
	if err != nil || command.Args[1] != "-e" || command.Args[len(command.Args)-1] != directory ||
		command.Args[len(command.Args)-2] != "51code-terminal" || command.Dir != directory {
		t.Fatalf("unsafe fallback: %+v, %v", command, err)
	}
	if _, err := commandFor("linux", "", directory, func(string) (string, error) {
		return "", errors.New("not installed")
	}); err == nil {
		t.Fatal("missing terminal was silently accepted")
	}
}

func TestTerminalRejectsMissingDirectoryAndApplication(t *testing.T) {
	if _, err := Start(filepath.Join(t.TempDir(), "missing"), ""); err == nil {
		t.Fatal("missing working directory was accepted")
	}
	if err := Validate(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing terminal application was accepted")
	}
	file := filepath.Join(t.TempDir(), "terminal")
	if err := os.WriteFile(file, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(file, ""); err == nil {
		t.Fatal("working directory accepted a regular file")
	}
}
