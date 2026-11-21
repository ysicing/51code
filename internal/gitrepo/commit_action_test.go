// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyCommitProtectsCurrentBranchAndChanges(t *testing.T) {
	ctx := context.Background()
	repo, dir, head, _ := mergeFixture(t, true)
	source := git(t, dir, "rev-parse", "topic^")
	for _, test := range []struct{ branch, head string }{
		{"topic", head},
		{"main", source},
	} {
		if err := repo.ApplyCommit(ctx, "cherry-pick", source, test.branch, test.head); err == nil {
			t.Fatal("accepted a destination different from the confirmation")
		}
	}
	write(t, dir, "main", "locally modified\n")
	if err := repo.ApplyCommit(ctx, "cherry-pick", source, "main", head); err == nil {
		t.Fatal("cherry-pick accepted a dirty working tree")
	}
	content, err := os.ReadFile(filepath.Join(dir, "main"))
	if err != nil || string(content) != "locally modified\n" || git(t, dir, "rev-parse", "HEAD") != head {
		t.Fatalf("owner's working state changed: %q, %v", content, err)
	}
}

func TestApplyCommitProtectsIgnoredFile(t *testing.T) {
	repo, dir, head, _ := mergeFixture(t, true)
	source := git(t, dir, "rev-parse", "topic^")
	write(t, dir, ".git/info/exclude", "/feature\n")
	write(t, dir, "feature", "keep ignored contents\n")
	if err := repo.ApplyCommit(context.Background(), "cherry-pick", source, "main", head); err == nil {
		t.Fatal("cherry-pick accepted an ignored file collision")
	}
	content, err := os.ReadFile(filepath.Join(dir, "feature"))
	if err != nil || string(content) != "keep ignored contents\n" || git(t, dir, "rev-parse", "HEAD") != head {
		t.Fatalf("ignored file changed: %q, %v", content, err)
	}
}

func TestApplyCommitLeavesRevertConflictForAbort(t *testing.T) {
	ctx := context.Background()
	repo, dir, _, _ := mergeFixture(t, true)
	git(t, dir, "merge", "--no-edit", "topic")
	merge := git(t, dir, "rev-parse", "HEAD")
	if err := repo.ApplyCommit(ctx, "revert", merge, "main", merge); err == nil {
		t.Fatal("reverted a merge without choosing a parent")
	}
	source := git(t, dir, "rev-parse", "topic^")
	if err := repo.ApplyCommit(ctx, "revert", source, "main", merge); err == nil {
		t.Fatal("expected a revert conflict")
	}
	operation, err := repo.operation(ctx)
	if err != nil || operation.Kind != "revert" || !strings.Contains(git(t, dir, "status", "--porcelain"), "UD feature") {
		t.Fatalf("revert conflict state missing: %+v, %v", operation, err)
	}
	if err := repo.ApplyCommit(ctx, "cherry-pick", source, "main", merge); err == nil {
		t.Fatal("cherry-pick accepted an in-progress revert")
	}
	if err := repo.OperationAction(ctx, operation.Kind, operation.Token, "abort"); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "status", "--porcelain"); got != "" || git(t, dir, "rev-parse", "HEAD") != merge {
		t.Fatalf("abort did not restore the original state: %s", got)
	}
}
