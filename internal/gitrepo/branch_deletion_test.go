// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBranchDeletionReviewAndExecution(t *testing.T) {
	for _, scenario := range []string{"plain", "linked", "ignored", "unmerged", "forced", "dirty-forced", "untracked-forced", "locked", "current", "main"} {
		t.Run(scenario, func(t *testing.T) {
			repo, dir, linked := worktreeFixture(t)
			branch, force := "topic", false
			switch scenario {
			case "plain":
				git(t, dir, "worktree", "remove", "--", linked)
			case "ignored":
				write(t, linked, "ignored/build", "generated\n")
			case "unmerged", "forced":
				git(t, linked, "commit", "--allow-empty", "-m", "unmerged")
				force = scenario == "forced"
			case "dirty-forced":
				force = true
				write(t, linked, "base.txt", "keep changes\n")
			case "untracked-forced":
				force = true
				write(t, linked, "untracked.txt", "keep file\n")
			case "locked":
				force = true
				git(t, dir, "worktree", "lock", linked)
			case "current":
				branch, force = "main", true
			case "main":
				var err error
				repo, err = Open(context.Background(), linked)
				if err != nil {
					t.Fatal(err)
				}
				branch, force = "main", true
			}
			review, err := repo.ReviewBranchDeletion(context.Background(), branch, force)
			if err != nil {
				t.Fatal(err)
			}
			blocked := scenario == "unmerged" || scenario == "dirty-forced" || scenario == "untracked-forced" ||
				scenario == "locked" || scenario == "current" || scenario == "main"
			if review.CanDelete == blocked || review.Unmerged != (scenario == "unmerged") ||
				review.Token == "" || (review.Worktree == nil) != (scenario == "plain") {
				t.Fatalf("unexpected deletion review: %+v", review)
			}
			head := git(t, dir, "rev-parse", "refs/heads/"+branch)
			if scenario == "plain" {
				git(t, dir, "config", "branch.topic.description", "删除后清理")
				git(t, dir, "config", "branch.topic-other.description", "保留其他分支配置")
			}
			err = repo.DeleteReviewedBranch(context.Background(), branch, force, review.Token)
			if blocked {
				if err == nil || review.Problem == "" {
					t.Fatal("unsafe deletion was allowed")
				}
				if git(t, dir, "rev-parse", "refs/heads/"+branch) != head {
					t.Fatal("blocked deletion changed the branch")
				}
				if _, err := os.Stat(linked); err != nil {
					t.Fatal("blocked deletion removed the worktree", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if git(t, dir, "branch", "--list", branch) != "" {
				t.Fatal("deletion kept the branch")
			}
			if scenario == "plain" {
				if config := git(t, dir, "config", "--local", "--list"); strings.Contains(config, "branch.topic.") ||
					!strings.Contains(config, "branch.topic-other.description=") {
					t.Fatalf("unexpected branch configuration after deletion: %s", config)
				}
			}
			if _, err := os.Stat(linked); !os.IsNotExist(err) {
				t.Fatal("deletion kept the linked directory", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "base.txt")); err != nil {
				t.Fatal("deletion affected the main worktree", err)
			}
		})
	}
}

func TestBranchDeletionRejectsMovementAtRefWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX Git command wrapper")
	}
	for _, scenario := range []string{"plain", "linked"} {
		t.Run(scenario, func(t *testing.T) {
			repo, dir, linked := worktreeFixture(t)
			if scenario == "plain" {
				git(t, dir, "worktree", "remove", "--", linked)
			}
			force := scenario == "linked"
			review, err := repo.ReviewBranchDeletion(context.Background(), "topic", force)
			if err != nil || !review.CanDelete {
				t.Fatalf("initial review failed: %+v, %v", review, err)
			}
			tree := git(t, dir, "rev-parse", "HEAD^{tree}")
			newHead := git(t, dir, "commit-tree", tree, "-p", review.Head, "-m", "concurrent update")
			git(t, dir, "config", "branch.topic.description", "keep this")
			actual, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			wrapper := filepath.Join(t.TempDir(), "git")
			script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    --delete|-D|update-ref)
      "$BRANCH_TEST_GIT" -C "$BRANCH_TEST_DIR" update-ref refs/heads/topic "$BRANCH_TEST_HEAD" || exit 1
      break
      ;;
  esac
done
exec "$BRANCH_TEST_GIT" "$@"
`
			if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BRANCH_TEST_GIT", actual)
			t.Setenv("BRANCH_TEST_DIR", dir)
			t.Setenv("BRANCH_TEST_HEAD", newHead)
			previous := currentExecutableOptions()
			ConfigureExecutable(ExecutableOptions{Source: "custom", Path: wrapper})
			t.Cleanup(func() { ConfigureExecutable(previous) })
			err = repo.DeleteReviewedBranch(context.Background(), "topic", force, review.Token)
			var partial *BranchDeletionPartialError
			if err == nil || errors.As(err, &partial) != (scenario == "linked") {
				t.Fatalf("unexpected result after concurrent movement: %v", err)
			}
			if head := git(t, dir, "rev-parse", "refs/heads/topic"); head != newHead {
				t.Fatalf("concurrent branch tip was not preserved: %s", head)
			}
			if description := git(t, dir, "config", "branch.topic.description"); description != "keep this" {
				t.Fatalf("configuration was changed after the rejected deletion: %q", description)
			}
		})
	}
}

func TestBranchDeletionProtectsBranchInDetachedWorktree(t *testing.T) {
	for marker, payload := range map[string]string{
		"rebase-merge/head-name":   "refs/heads/topic\n",
		"rebase-apply/head-name":   "refs/heads/topic\n",
		"rebase-merge/update-refs": "refs/heads/topic\n",
		"BISECT_START":             "topic\n",
	} {
		t.Run(marker, func(t *testing.T) {
			repo, dir, linked := worktreeFixture(t)
			git(t, linked, "switch", "--detach")
			gitDir := git(t, linked, "rev-parse", "--absolute-git-dir")
			write(t, gitDir, marker, payload)
			review, err := repo.ReviewBranchDeletion(context.Background(), "topic", true)
			if err != nil {
				t.Fatal(err)
			}
			if review.CanDelete || !strings.Contains(review.Problem, "变基或二分查找") {
				t.Fatalf("expected branch operation to block the review: %+v", review)
			}
			err = repo.DeleteReviewedBranch(context.Background(), "topic", true, review.Token)
			if err == nil || !strings.Contains(err.Error(), "变基或二分查找") {
				t.Fatalf("expected branch operation protection: %v", err)
			}
			if head := git(t, dir, "rev-parse", "refs/heads/topic"); head != review.Head {
				t.Fatalf("branch in use was not preserved: %s", head)
			}
		})
	}
}

func TestBranchDeletionUsesUpstreamMergeRule(t *testing.T) {
	for _, scenario := range []string{"merged-upstream", "unmerged-upstream", "gone-upstream"} {
		t.Run(scenario, func(t *testing.T) {
			repo, dir, linked := worktreeFixture(t)
			base := git(t, dir, "rev-parse", "HEAD")
			git(t, linked, "commit", "--allow-empty", "-m", "topic")
			tip := git(t, linked, "rev-parse", "HEAD")
			git(t, dir, "config", "remote.origin.url", "unused")
			git(t, dir, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
			git(t, dir, "config", "branch.topic.remote", "origin")
			git(t, dir, "config", "branch.topic.merge", "refs/heads/topic")
			if scenario == "merged-upstream" {
				git(t, dir, "update-ref", "refs/remotes/origin/topic", tip)
			} else {
				git(t, dir, "merge", "--ff-only", "topic")
				if scenario == "unmerged-upstream" {
					git(t, dir, "update-ref", "refs/remotes/origin/topic", base)
				}
			}
			review, err := repo.ReviewBranchDeletion(context.Background(), "topic", false)
			if err != nil {
				t.Fatal(err)
			}
			if review.CanDelete == (scenario == "unmerged-upstream") {
				t.Fatalf("upstream merge rule differs from Git: %+v", review)
			}
			err = repo.DeleteReviewedBranch(context.Background(), "topic", false, review.Token)
			if scenario == "unmerged-upstream" {
				if err == nil {
					t.Fatal("deleted a branch unmerged into its upstream")
				}
				if _, err := os.Stat(linked); err != nil {
					t.Fatal("removed worktree before checking upstream", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBranchDeletionRejectsStaleReview(t *testing.T) {
	for _, change := range []string{"source-head", "target-head", "untracked", "lock", "force", "upstream", "attach", "replacement"} {
		t.Run(change, func(t *testing.T) {
			repo, dir, linked := worktreeFixture(t)
			if change == "attach" {
				git(t, dir, "worktree", "remove", "--", linked)
			}
			review, err := repo.ReviewBranchDeletion(context.Background(), "topic", false)
			if err != nil || !review.CanDelete {
				t.Fatal("initial review failed", err)
			}
			force := false
			switch change {
			case "source-head":
				git(t, linked, "commit", "--allow-empty", "-m", "changed")
			case "target-head":
				git(t, dir, "commit", "--allow-empty", "-m", "changed")
			case "untracked":
				write(t, linked, "keep", "keep\n")
			case "lock":
				git(t, dir, "worktree", "lock", linked)
			case "force":
				force = true
			case "upstream":
				git(t, dir, "branch", "other")
				git(t, dir, "branch", "--set-upstream-to=other", "topic")
			case "attach":
				git(t, dir, "worktree", "add", "--", linked, "topic")
			case "replacement":
				if err := os.Rename(linked, linked+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(linked, 0755); err != nil {
					t.Fatal(err)
				}
				git(t, linked, "init", "-b", "unrelated")
				write(t, linked, "precious", "keep replacement\n")
			}
			if err := repo.DeleteReviewedBranch(context.Background(), "topic", force, review.Token); err == nil {
				t.Fatal("accepted a stale or differently authorized review")
			}
			if _, err := os.Stat(linked); err != nil {
				t.Fatal("stale review removed worktree", err)
			}
			if git(t, dir, "branch", "--list", "topic") == "" {
				t.Fatal("stale review deleted branch")
			}
		})
	}
}

func TestBranchDeletionReportsPartialFailure(t *testing.T) {
	repo, dir, linked := worktreeFixture(t)
	review, err := repo.ReviewBranchDeletion(context.Background(), "topic", false)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, ".git/refs/heads/topic.lock", "held\n")
	err = repo.DeleteReviewedBranch(context.Background(), "topic", false, review.Token)
	var partial *BranchDeletionPartialError
	if !errors.As(err, &partial) || !strings.Contains(err.Error(), "工作树已移除") {
		t.Fatalf("partial result not reported: %v", err)
	}
	if _, err := os.Stat(linked); !os.IsNotExist(err) {
		t.Fatal("expected worktree removal before reference lock failure", err)
	}
	if git(t, dir, "rev-parse", "refs/heads/topic") != review.Head {
		t.Fatal("partial failure lost branch commits")
	}
}

func TestBranchDeletionCancellationAndMissingConfirmation(t *testing.T) {
	repo, dir, linked := worktreeFixture(t)
	review, err := repo.ReviewBranchDeletion(context.Background(), "topic", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteReviewedBranch(context.Background(), "topic", true, ""); err == nil {
		t.Fatal("accepted a missing confirmation token")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := repo.DeleteReviewedBranch(ctx, "topic", true, review.Token); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not honored", err)
	}
	if _, err := os.Stat(linked); err != nil || git(t, dir, "branch", "--list", "topic") == "" {
		t.Fatal("cancellation changed worktree or branch", err)
	}
}

func TestBranchDeletionRejectsMultipleLinkedWorktrees(t *testing.T) {
	repo, dir, linked := worktreeFixture(t)
	second := filepath.Join(t.TempDir(), "second")
	git(t, dir, "worktree", "add", "--force", "--", second, "topic")
	review, err := repo.ReviewBranchDeletion(context.Background(), "topic", true)
	if err != nil {
		t.Fatal(err)
	}
	if review.CanDelete || !strings.Contains(review.Problem, "多个工作树") {
		t.Fatalf("multiple worktrees were not explicitly blocked: %+v", review)
	}
	if err := repo.DeleteReviewedBranch(context.Background(), "topic", true, review.Token); err == nil {
		t.Fatal("deleted more than one worktree without separate confirmation")
	}
	for _, path := range []string{linked, second} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("multiple-worktree rejection changed directories", err)
		}
	}
}
