// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLostCommitsFromReflog(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	if result, err := repo.LostCommits(ctx); err != nil || len(result.Commits) != 0 || result.Limited {
		t.Fatalf("unborn repository = %+v, %v", result, err)
	}
	git(t, dir, "commit", "--allow-empty", "-m", "base")
	base := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "switch", "-c", "discarded")
	git(t, dir, "commit", "--allow-empty", "-m", "retained by tag")
	git(t, dir, "tag", "retained")
	git(t, dir, "commit", "--allow-empty", "-m", "丢失的提交")
	lost := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "switch", "main")
	git(t, dir, "branch", "-D", "discarded")
	git(t, dir, "switch", "--detach", lost)
	git(t, dir, "switch", "main")

	result, err := repo.LostCommits(ctx)
	if err != nil || result.Limited || len(result.Commits) != 1 {
		t.Fatalf("lost commits = %+v, %v", result, err)
	}
	recovered := result.Commits[0]
	if recovered.Hash != lost || recovered.Subject != "丢失的提交" || recovered.Ref != "HEAD" || recovered.Action == "" {
		t.Fatalf("unexpected reflog entry: %+v", recovered)
	}
	if _, err := time.Parse(time.RFC3339, recovered.Date); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "draft.txt", "local draft\n")
	if err := repo.CreateBranchAt(ctx, "recovered", recovered.Hash); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "rev-parse", "refs/heads/recovered"); got != lost {
		t.Fatalf("recovery branch = %s, want %s", got, lost)
	}
	if current := git(t, dir, "rev-parse", "HEAD"); current != base {
		t.Fatalf("recovery changed HEAD: %s", current)
	}
	if result, err = repo.LostCommits(ctx); err != nil || len(result.Commits) != 0 {
		t.Fatalf("already recovered commit remained listed: %+v, %v", result, err)
	}
}

func TestLostCommitsScansLocalBranchReflogAndPreservesWorktreeHeads(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	git(t, dir, "commit", "--allow-empty", "-m", "base")
	tree := git(t, dir, "rev-parse", "HEAD^{tree}")
	lost := git(t, dir, "commit-tree", tree, "-p", "HEAD", "-m", "moved branch")
	git(t, dir, "branch", "moved", lost)
	git(t, dir, "branch", "-f", "moved", "main")
	result, err := repo.LostCommits(ctx)
	if err != nil || len(result.Commits) != 1 || result.Commits[0].Hash != lost || result.Commits[0].Ref != "moved" {
		t.Fatalf("lost branch reflog commit = %+v, %v", result, err)
	}
	git(t, dir, "worktree", "add", "--detach", filepath.Join(t.TempDir(), "linked"), lost)
	if result, err := repo.LostCommits(ctx); err != nil || len(result.Commits) != 0 {
		t.Fatalf("detached worktree HEAD was treated as lost: %+v, %v", result, err)
	}
}

func TestLostCommitsScanLimitAndCancellation(t *testing.T) {
	repo, dir := fixture(t)
	git(t, dir, "commit", "--allow-empty", "-m", "base")
	base := git(t, dir, "rev-parse", "HEAD")
	tree := git(t, dir, "rev-parse", "HEAD^{tree}")
	lost := git(t, dir, "commit-tree", tree, "-p", "HEAD", "-m", "lost")
	git(t, dir, "branch", "moved", base)
	var updates strings.Builder
	for index := range ReflogRecoveryLimit + 2 {
		hash := lost
		if index%2 != 0 {
			hash = base
		}
		updates.WriteString("start\nupdate refs/heads/moved " + hash + "\ncommit\n")
	}
	if _, err := run(context.Background(), dir, updates.String(), 15*time.Second, "update-ref", "--stdin"); err != nil {
		t.Fatal(err)
	}
	result, err := repo.LostCommits(context.Background())
	if err != nil || !result.Limited || len(result.Commits) != 1 || result.Commits[0].Hash != lost {
		t.Fatalf("bounded, deduplicated scan = %+v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repo.LostCommits(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan = %v", err)
	}
}
