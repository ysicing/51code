// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInitRejectsExistingGitMetadata(t *testing.T) {
	_, worktree := fixture(t)
	nested := filepath.Join(worktree, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	bare := t.TempDir()
	git(t, bare, "init", "--bare")
	broken := t.TempDir()
	write(t, broken, ".git", "invalid metadata\n")
	for _, directory := range []string{worktree, nested, bare, broken} {
		t.Run(directory, func(t *testing.T) {
			canonical, err := filepath.EvalSymlinks(directory)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := InitCandidate(context.Background(), canonical)
			if err != nil || candidate != "" {
				t.Fatalf("existing Git metadata offered initialization: %q, %v", candidate, err)
			}
			if err := Init(context.Background(), canonical); err == nil {
				t.Fatal("initialization accepted existing Git metadata")
			}
		})
	}
}

func TestWriteInitialGitignorePreservesExistingEntries(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, ".gitignore")
	if err := WriteInitialGitignore(ctx, directory, " \n"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("blank template should not create a file: %v", err)
	}
	const original = "*.local\n!keep.local\n"
	for _, template := range []string{original, "replacement\n"} {
		if err := WriteInitialGitignore(ctx, directory, template); err != nil {
			t.Fatal(err)
		}
		if content, err := os.ReadFile(path); err != nil || string(content) != original {
			t.Fatalf("unexpected .gitignore content %q: %v", content, err)
		}
	}
	linkDirectory := t.TempDir()
	link := filepath.Join(linkDirectory, ".gitignore")
	target := filepath.Join(linkDirectory, "missing")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := WriteInitialGitignore(ctx, linkDirectory, original); err != nil {
		t.Fatal(err)
	}
	if value, err := os.Readlink(link); err != nil || value != target {
		t.Fatalf("existing link was replaced: %q, %v", value, err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("symlink target was created: %v", err)
	}
}
