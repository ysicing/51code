// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestSSHHostAliasesReadsIncludesAndLiteralAliases(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(dir, "conf.d"), 0700); err != nil {
		t.Fatal(err)
	}
	configs := map[string]string{
		"config":                 "HOST=dev DEV staging *.example !excluded # note\nInclude conf.d/*.conf \"~/extra hosts\"\nHost last",
		"conf.d/01.conf":         "Include config\nHost alpha",
		"conf.d/02.conf":         "Host beta",
		"conf.d/ignored.example": "Host ignored",
	}
	for name, content := range configs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "extra hosts"), []byte("Host 'gamma' user@host # note"), 0600); err != nil {
		t.Fatal(err)
	}
	want := []string{"dev", "staging", "alpha", "beta", "gamma", "last"}
	if got := SSHHostAliases(t.Context()); !slices.Equal(got, want) {
		t.Fatalf("aliases = %q, want %q", got, want)
	}
}
