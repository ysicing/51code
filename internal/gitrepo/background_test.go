// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchDirectoriesIncludesLinkedWorktreeMetadata(t *testing.T) {
	main, _, linkedPath := worktreeFixture(t)
	linked, err := Open(context.Background(), linkedPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range []*Repository{main, linked} {
		directories, err := repo.WatchDirectories(context.Background())
		if err != nil || len(directories) != 3 || directories[0] != repo.Path ||
			filepath.Clean(directories[2]) != filepath.Join(main.Path, ".git") {
			t.Fatalf("incorrect worktree watch roots: %q, %v", directories, err)
		}
		if repo == linked && directories[1] == directories[2] {
			t.Fatal("linked worktree's independent HEAD/index directory was lost")
		}
	}
}

func TestBackgroundFetchUpdatesRemoteWithoutChangingWorktree(t *testing.T) {
	source, sourcePath := fixture(t)
	write(t, sourcePath, "base.txt", "base\n")
	git(t, sourcePath, "add", ".")
	git(t, sourcePath, "commit", "-m", "base")
	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, sourcePath, "clone", "--bare", sourcePath, remote)
	local := filepath.Join(t.TempDir(), "local")
	git(t, sourcePath, "clone", remote, local)
	repo, err := Open(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	git(t, sourcePath, "remote", "add", "origin", remote)
	git(t, sourcePath, "commit", "--allow-empty", "-m", "new remote commit")
	git(t, sourcePath, "push", "origin", "main")
	head := git(t, local, "rev-parse", "HEAD")
	write(t, local, "base.txt", "preserve local edits\n")
	before := git(t, local, "diff")
	if err := repo.FetchInBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if git(t, local, "rev-parse", "origin/main") != git(t, source.Path, "rev-parse", "HEAD") ||
		git(t, local, "rev-parse", "HEAD") != head || git(t, local, "diff") != before {
		t.Fatal("background fetch changed HEAD or working edits, or failed to update the remote reference")
	}
}

func TestBackgroundFetchLockWaitIsCancelable(t *testing.T) {
	repo, _ := fixture(t)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- repo.FetchInBackground(ctx) }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected canceled fetch result: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("background fetch ignored cancellation while waiting for the repository lock")
	}
}
