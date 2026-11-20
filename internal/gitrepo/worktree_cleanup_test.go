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
	"slices"
	"strings"
	"testing"
)

func TestWorktreeCleanupRequiresLocalAncestry(t *testing.T) {
	repo, dir, path := worktreeFixture(t)
	ctx := context.Background()
	base := git(t, dir, "rev-parse", "HEAD")
	write(t, path, "feature.txt", "feature\n")
	git(t, path, "add", ".")
	git(t, path, "commit", "-m", "feature")
	tip := git(t, path, "rev-parse", "HEAD")
	git(t, dir, "tag", "retained", tip)
	git(t, dir, "update-ref", "refs/remotes/origin/retained", tip)
	git(t, dir, "symbolic-ref", "refs/heads/topic-alias", "refs/heads/topic")
	for _, squash := range []bool{false, true} {
		if squash {
			git(t, dir, "merge", "--squash", "topic")
			git(t, dir, "commit", "-m", "squashed feature")
		}
		items, err := repo.Worktrees(ctx)
		if err != nil {
			t.Fatal(err)
		}
		item := items[1]
		if len(item.ContainedIn) != 0 || item.CleanupCandidate {
			t.Fatalf("own ref, alias, tag, remote or squash counted as containment: %+v", item)
		}
		review, err := repo.ReviewWorktreeCleanup(ctx, path)
		if err != nil || review.CanRemove {
			t.Fatalf("unproven cleanup review: %+v, %v", review, err)
		}
		if !worktreeReview(t, repo, path).CanRemove {
			t.Fatal("ordinary removal must remain independent of ancestry")
		}
	}
	git(t, dir, "merge", "--no-ff", "--no-edit", "topic")
	git(t, dir, "branch", "also-contained")
	items, err := repo.Worktrees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !items[1].CleanupCandidate ||
		!slices.Equal(items[1].ContainedIn, []string{"refs/heads/also-contained", "refs/heads/main"}) {
		t.Fatalf("real merge not identified: %+v", items[1])
	}
	git(t, path, "checkout", "--detach")
	items, err = repo.Worktrees(ctx)
	if err != nil || !items[1].CleanupCandidate || !slices.Contains(items[1].ContainedIn, "refs/heads/main") {
		t.Fatalf("detached source with a live containing branch: %+v, %v", items, err)
	}
	if git(t, dir, "rev-parse", "topic") != tip || base == tip {
		t.Fatal("ancestry inspection changed source commits")
	}
}

func TestWorktreeCleanupCandidateSafety(t *testing.T) {
	for _, scenario := range []string{
		"clean", "ignored", "tracked", "untracked", "locked", "missing",
		"operation", "assume-unchanged", "skip-worktree", "current", "main", "unborn",
	} {
		t.Run(scenario, func(t *testing.T) {
			repo, dir, path := worktreeFixture(t)
			switch scenario {
			case "ignored":
				write(t, path, "ignored/keep", "ignored data")
			case "tracked":
				write(t, path, "base.txt", "precious edits")
			case "untracked":
				write(t, path, "keep", "precious data")
			case "locked":
				git(t, dir, "worktree", "lock", path)
			case "missing":
				if err := os.Rename(path, path+"-moved"); err != nil {
					t.Fatal(err)
				}
			case "operation":
				metadata := git(t, path, "rev-parse", "--absolute-git-dir")
				write(t, metadata, "MERGE_HEAD", git(t, path, "rev-parse", "HEAD")+"\n")
			case "assume-unchanged", "skip-worktree":
				git(t, path, "update-index", "--"+scenario, "base.txt")
				write(t, path, "base.txt", "hidden edits")
			case "current":
				var err error
				repo, err = Open(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
			case "main":
				path = repo.Path
			case "unborn":
				git(t, path, "switch", "--orphan", "empty")
			}
			items, err := repo.Worktrees(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.Path == path {
					want := scenario == "clean" || scenario == "ignored"
					if item.CleanupCandidate != want {
						t.Fatalf("candidate=%t, want %t: %+v", item.CleanupCandidate, want, item)
					}
					review, err := repo.ReviewWorktreeCleanup(context.Background(), path)
					if err != nil || review.CanRemove != want {
						t.Fatalf("cleanup review: %+v, %v", review, err)
					}
					return
				}
			}
			t.Fatal("worktree disappeared from list")
		})
	}
}

func TestWorktreeCleanupRechecksConfirmationAndRemoval(t *testing.T) {
	for _, change := range []string{
		"target-deleted", "target-rewritten", "source-head", "source-branch",
		"ignored", "untracked", "locked", "metadata", "invalid-token", "manual-token",
	} {
		t.Run(change, func(t *testing.T) {
			repo, dir, path := worktreeFixture(t)
			git(t, path, "commit", "--allow-empty", "-m", "topic")
			git(t, dir, "branch", "landed", "topic")
			ctx := context.Background()
			review, err := repo.ReviewWorktreeCleanup(ctx, path)
			if err != nil || !review.CanRemove || !review.Worktree.CleanupCandidate {
				t.Fatalf("initial cleanup review: %+v, %v", review, err)
			}
			switch change {
			case "target-deleted":
				git(t, dir, "branch", "-D", "landed")
			case "target-rewritten":
				git(t, dir, "update-ref", "refs/heads/landed", git(t, dir, "rev-parse", "main"))
			case "source-head":
				git(t, path, "commit", "--allow-empty", "-m", "later")
			case "source-branch":
				git(t, path, "switch", "-c", "other")
			case "ignored":
				write(t, path, "ignored/keep", "new ignored data")
			case "untracked":
				write(t, path, "keep", "new data")
			case "locked":
				git(t, dir, "worktree", "lock", path)
			case "metadata":
				metadata := git(t, path, "rev-parse", "--absolute-git-dir")
				backup := filepath.Join(t.TempDir(), "original")
				if err := os.Rename(metadata, backup); err != nil {
					t.Fatal(err)
				}
				if err := os.CopyFS(metadata, os.DirFS(backup)); err != nil {
					t.Fatal(err)
				}
			case "invalid-token":
				review.Token = ""
			case "manual-token":
				review.Token = worktreeReview(t, repo, path).Token
			}
			if change == "target-deleted" || change == "target-rewritten" || change == "source-head" {
				fresh, err := repo.ReviewWorktreeCleanup(ctx, path)
				if err != nil || fresh.CanRemove {
					t.Fatalf("confirmation retained stale ancestry: %+v, %v", fresh, err)
				}
			}
			if err := repo.RemoveWorktreeCleanup(ctx, path, review.Token); err == nil {
				t.Fatal("cleanup accepted stale or invalid confirmation")
			}
			if _, err := os.Stat(filepath.Join(path, "base.txt")); err != nil {
				t.Fatal("rejected cleanup changed source files:", err)
			}
			if git(t, dir, "branch", "--list", "topic") == "" {
				t.Fatal("cleanup deleted source branch")
			}
		})
	}
}

func TestWorktreeCleanupRemovesOnlyReviewedDirectoryAndRetainsBranch(t *testing.T) {
	repo, dir, path := worktreeFixture(t)
	write(t, path, "ignored/keep", "ignored data")
	ctx := context.Background()
	head := git(t, dir, "rev-parse", "topic")
	review, err := repo.ReviewWorktreeCleanup(ctx, path)
	if err != nil || !review.CanRemove || !review.Worktree.Ignored {
		t.Fatalf("review: %+v, %v", review, err)
	}
	if err := repo.RemoveWorktree(ctx, path, review.Token); err == nil {
		t.Fatal("ordinary removal accepted cleanup token")
	}
	if err := repo.RemoveWorktreeCleanup(ctx, path, review.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("reviewed directory not removed: %v", err)
	}
	if git(t, dir, "rev-parse", "topic") != head || git(t, dir, "rev-parse", "HEAD") != head {
		t.Fatal("cleanup changed retained branch or current HEAD")
	}
	if _, err := os.Stat(filepath.Join(dir, "base.txt")); err != nil {
		t.Fatal("cleanup touched current worktree:", err)
	}
}

func TestWorktreeCleanupRechecksAncestryAfterIdentityChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX Git command wrapper")
	}
	for _, change := range []string{"target-deleted", "source-head"} {
		t.Run(change, func(t *testing.T) {
			repo, dir, path := worktreeFixture(t)
			// 避免换行路径触发 revParsePaths 的逐项回退，确保仅在目录身份复核时注入。
			source := filepath.Join(filepath.Dir(path), "source")
			git(t, dir, "worktree", "move", path, source)
			path = source
			git(t, path, "commit", "--allow-empty", "-m", "topic")
			git(t, dir, "branch", "landed", "topic")
			tree := git(t, path, "rev-parse", "HEAD^{tree}")
			tip := git(t, path, "rev-parse", "HEAD")
			next := git(t, dir, "commit-tree", tree, "-p", tip, "-m", "concurrent source update")
			review, err := repo.ReviewWorktreeCleanup(context.Background(), path)
			if err != nil || !review.CanRemove {
				t.Fatalf("review: %+v, %v", review, err)
			}
			actual, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			wrapper := filepath.Join(t.TempDir(), "git")
			script := `#!/bin/sh
root=
previous=
for arg in "$@"; do
  if [ "$previous" = -C ]; then root=$arg; fi
  previous=$arg
done
case "$*" in
  *" rev-parse --path-format=absolute --git-common-dir")
    if [ "$root" = "$CLEANUP_TEST_SOURCE" ]; then
      if [ "$CLEANUP_TEST_CHANGE" = source-head ]; then
        "$CLEANUP_TEST_GIT" -C "$CLEANUP_TEST_ROOT" update-ref refs/heads/topic "$CLEANUP_TEST_HEAD" || exit 1
      else
        "$CLEANUP_TEST_GIT" -C "$CLEANUP_TEST_ROOT" update-ref -d refs/heads/landed || exit 1
      fi
    fi
    ;;
esac
exec "$CLEANUP_TEST_GIT" "$@"
`
			if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CLEANUP_TEST_GIT", actual)
			t.Setenv("CLEANUP_TEST_SOURCE", path)
			t.Setenv("CLEANUP_TEST_ROOT", dir)
			t.Setenv("CLEANUP_TEST_CHANGE", change)
			t.Setenv("CLEANUP_TEST_HEAD", next)
			previous := currentExecutableOptions()
			ConfigureExecutable(ExecutableOptions{Source: "custom", Path: wrapper})
			t.Cleanup(func() { ConfigureExecutable(previous) })
			err = repo.RemoveWorktreeCleanup(context.Background(), path, review.Token)
			problem := "包含关系"
			if change == "source-head" {
				problem = "状态已改变"
				if git(t, path, "rev-parse", "HEAD") != next {
					t.Fatal("test did not move source during identity checks")
				}
			} else if git(t, dir, "branch", "--list", "landed") != "" {
				t.Fatal("test did not move target during identity checks")
			}
			if err == nil || !strings.Contains(err.Error(), problem) {
				t.Fatalf("missing final source/ancestry check: %v", err)
			}
			if _, err := os.Stat(filepath.Join(path, "base.txt")); err != nil {
				t.Fatal("cleanup removed directory after concurrent change:", err)
			}
		})
	}
}

func TestWorktreeCleanupSharesQueriesAndKeepsManualRemovalOnQueryFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX Git command wrapper")
	}
	for _, scenario := range []string{"shared-head", "query-failure"} {
		t.Run(scenario, func(t *testing.T) {
			fail := scenario == "query-failure"
			repo, dir, path := worktreeFixture(t)
			git(t, dir, "worktree", "add", "-b", "another", filepath.Join(t.TempDir(), "another"))
			actual, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(t.TempDir(), "queries")
			wrapper := filepath.Join(t.TempDir(), "git")
			script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    --contains=*)
      printf 'query\n' >> "$CLEANUP_TEST_LOG"
      if [ "$CLEANUP_TEST_FAIL" = yes ]; then
        printf 'ancestry unavailable\n' >&2
        exit 1
      fi
      ;;
  esac
done
exec "$CLEANUP_TEST_GIT" "$@"
`
			if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CLEANUP_TEST_GIT", actual)
			t.Setenv("CLEANUP_TEST_LOG", log)
			if fail {
				t.Setenv("CLEANUP_TEST_FAIL", "yes")
			} else {
				t.Setenv("CLEANUP_TEST_FAIL", "no")
			}
			previous := currentExecutableOptions()
			ConfigureExecutable(ExecutableOptions{Source: "custom", Path: wrapper})
			t.Cleanup(func() { ConfigureExecutable(previous) })
			items, err := repo.Worktrees(context.Background())
			if err != nil || len(items) != 3 {
				t.Fatalf("list unavailable: %+v, %v", items, err)
			}
			queries, err := os.ReadFile(log)
			if err != nil || string(queries) != "query\n" {
				t.Fatalf("same HEAD should use one ancestry subprocess: %q, %v", queries, err)
			}
			for _, item := range items {
				if fail {
					if item.CleanupCandidate || item.ContainmentProblem == "" || item.Problem != "" {
						t.Fatalf("query failure did not fail closed independently: %+v", item)
					}
				} else if len(item.ContainedIn) != 2 || slices.Contains(item.ContainedIn, item.Branch) {
					t.Fatalf("cached query did not exclude each worktree's own branch: %+v", item)
				}
			}
			if !worktreeReview(t, repo, path).CanRemove {
				t.Fatal("ancestry query disabled ordinary removal")
			}
			if fail {
				if _, err := repo.ReviewWorktreeCleanup(context.Background(), path); err == nil {
					t.Fatal("cleanup review accepted failed ancestry query")
				}
			}
		})
	}
}

func TestWorktreeCleanupCancellationPreservesDirectory(t *testing.T) {
	repo, _, path := worktreeFixture(t)
	review, err := repo.ReviewWorktreeCleanup(context.Background(), path)
	if err != nil || !review.CanRemove {
		t.Fatalf("review: %+v, %v", review, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repo.ReviewWorktreeCleanup(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel review: %v", err)
	}
	if err := repo.RemoveWorktreeCleanup(ctx, path, review.Token); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "base.txt")); err != nil {
		t.Fatal("canceled cleanup changed directory:", err)
	}
}
