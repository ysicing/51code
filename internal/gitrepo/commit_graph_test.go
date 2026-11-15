// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func commitGraphExists(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, ".git", "objects", "info", "commit-graph"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestEnsureCommitGraphWritesOnce(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	for index, name := range []string{"a.txt", "中文/b.txt"} {
		write(t, dir, name, strings.Repeat("x", index+1))
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-m", name)
	}
	written, err := repo.EnsureCommitGraph(ctx)
	if err != nil || !written || !commitGraphExists(t, dir) {
		t.Fatalf("written = %v, err = %v", written, err)
	}
	git(t, dir, "commit-graph", "verify")
	// 已有 commit-graph 时不重复写入，后续增量交给 fetch 与 gc。
	if written, err := repo.EnsureCommitGraph(ctx); err != nil || written {
		t.Fatalf("second call written = %v, err = %v", written, err)
	}
	// 写入后历史结果保持不变。
	if got := git(t, dir, "log", "--format=%H", "--", "中文/b.txt"); len(strings.Fields(got)) != 1 {
		t.Fatalf("path history = %q", got)
	}
}

func TestEnsureCommitGraphSkips(t *testing.T) {
	ctx := context.Background()
	t.Run("disabled by config", func(t *testing.T) {
		repo, dir := fixture(t)
		write(t, dir, "a.txt", "one\n")
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-m", "base")
		git(t, dir, "config", "core.commitGraph", "false")
		if written, err := repo.EnsureCommitGraph(ctx); err != nil || written || commitGraphExists(t, dir) {
			t.Fatalf("written = %v, err = %v", written, err)
		}
	})
	t.Run("shallow clone", func(t *testing.T) {
		_, source := fixture(t)
		for _, value := range []string{"one\n", "two\n"} {
			write(t, source, "a.txt", value)
			git(t, source, "add", ".")
			git(t, source, "commit", "-m", value)
		}
		dir := filepath.Join(t.TempDir(), "shallow")
		git(t, source, "clone", "--depth=1", "file://"+source, dir)
		repo, err := Open(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		if written, err := repo.EnsureCommitGraph(ctx); err != nil || written {
			t.Fatalf("written = %v, err = %v", written, err)
		}
	})
	t.Run("existing split chain", func(t *testing.T) {
		repo, dir := fixture(t)
		write(t, dir, "a.txt", "one\n")
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-m", "base")
		git(t, dir, "commit-graph", "write", "--reachable", "--split")
		if written, err := repo.EnsureCommitGraph(ctx); err != nil || written || commitGraphExists(t, dir) {
			t.Fatalf("written = %v, err = %v", written, err)
		}
	})
}

func TestEnsureCommitGraphFromLinkedWorktree(t *testing.T) {
	_, dir := fixture(t)
	write(t, dir, "a.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, dir, "worktree", "add", "-b", "linked", linked)
	repo, err := Open(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	// 对象库属于主仓库，commit-graph 必须写到公共目录而不是 worktree 私有目录。
	if written, err := repo.EnsureCommitGraph(context.Background()); err != nil || !written || !commitGraphExists(t, dir) {
		t.Fatalf("written = %v, err = %v", written, err)
	}
}

func lockedCommitGraphFixture(t *testing.T, age time.Duration) (*Repository, string, string) {
	t.Helper()
	repo, dir := fixture(t)
	write(t, dir, "a.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	lock := filepath.Join(dir, ".git", "objects", "info", "commit-graph.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0644); err != nil {
		t.Fatal(err)
	}
	modified := time.Now().Add(-age)
	if err := os.Chtimes(lock, modified, modified); err != nil {
		t.Fatal(err)
	}
	return repo, dir, lock
}

func TestEnsureCommitGraphKeepsRecentLock(t *testing.T) {
	repo, _, lock := lockedCommitGraphFixture(t, staleCommitGraphLockAge-time.Minute)
	// 未超过阈值可能是其他 Git 进程正在写入，放弃本次，不能删除或覆盖对方的锁。
	if written, err := repo.EnsureCommitGraph(context.Background()); err == nil || written {
		t.Fatalf("written = %v, err = %v", written, err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("lock file must be left untouched:", err)
	}
}

func TestEnsureCommitGraphRemovesStaleLock(t *testing.T) {
	repo, dir, lock := lockedCommitGraphFixture(t, staleCommitGraphLockAge+time.Minute)
	if written, err := repo.EnsureCommitGraph(context.Background()); err != nil || !written || !commitGraphExists(t, dir) {
		t.Fatalf("written = %v, err = %v", written, err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("stale lock should be removed:", err)
	}
	git(t, dir, "commit-graph", "verify")
	// 清理后用户的 gc 恢复正常。
	git(t, dir, "gc", "--quiet")
}

func TestRemoveStaleCommitGraphLocksRequiresLockFailure(t *testing.T) {
	_, _, lock := lockedCommitGraphFixture(t, staleCommitGraphLockAge+time.Minute)
	info := filepath.Dir(lock)
	// 其他原因的失败不能触发删除。
	if removeStaleCommitGraphLocks(info, errors.New("fatal: some other failure")) {
		t.Fatal("unrelated failure removed lock")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal(err)
	}
}
