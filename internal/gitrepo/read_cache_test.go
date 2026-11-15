// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// gitCallLog 记录被测代码启动的 Git 子命令，用于确认缓存确实省去了进程。
func gitCallLog(t *testing.T) (reset func(), calls func(command string) int) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("XSOURCE_GIT_LOG", log)
	installHistoryGitWrapper(t, `printf '%s\n' "$*" >> "$XSOURCE_GIT_LOG"; exec "$XSOURCE_REAL_GIT" "$@"`+"\n")
	reset = func() {
		if err := os.WriteFile(log, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls = func(command string) int {
		data, err := os.ReadFile(log)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.Count(string(data), " "+command+" ")
	}
	return reset, calls
}

func cacheFixture(t *testing.T) (*Repository, string, []string) {
	t.Helper()
	repo, dir := fixture(t)
	write(t, dir, "a.txt", "one\n")
	write(t, dir, "b.txt", strings.Repeat("moved\n", 20))
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	base := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	git(t, dir, "switch", "-c", "side")
	write(t, dir, "side.txt", "side\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "side")
	git(t, dir, "switch", "main")
	write(t, dir, "a.txt", "two\n")
	git(t, dir, "mv", "b.txt", "c.txt")
	write(t, dir, "d.txt", "new\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "change")
	change := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	git(t, dir, "merge", "--no-ff", "-m", "merge", "side")
	merge := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	return repo, dir, []string{base, change, merge}
}

func TestCommitFileDiffReusesCommitFiles(t *testing.T) {
	repo, _, commits := cacheFixture(t)
	reset, calls := gitCallLog(t)
	ctx := context.Background()
	for _, hash := range commits {
		files, err := repo.CommitFiles(ctx, hash)
		if err != nil || len(files) == 0 {
			t.Fatal(hash, files, err)
		}
		reset()
		for _, file := range files {
			got, err := repo.CommitFileDiff(ctx, hash, file.Path)
			if err != nil {
				t.Fatal(hash, file.Path, err)
			}
			// 无缓存的新实例走完整读取路径，结果必须一致。
			want, err := (&Repository{Path: repo.Path}).CommitFileDiff(ctx, hash, file.Path)
			if err != nil {
				t.Fatal(hash, file.Path, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s %s:\n got %+v\nwant %+v", hash, file.Path, got, want)
			}
		}
		// 新实例各读取一次；复用实例不再重复类型检查、diff-tree，父提交也只读一次。
		if got, want := calls("diff-tree"), len(files); got != want {
			t.Fatalf("%s diff-tree calls = %d, want %d", hash, got, want)
		}
		if got, want := calls("rev-list"), len(files)+1; got != want {
			t.Fatalf("%s rev-list calls = %d, want %d", hash, got, want)
		}
	}
	// 缓存属于最近一次 CommitFiles；查看其他提交时仍从 Git 读取。
	got, err := repo.CommitFileDiff(ctx, commits[1], "c.txt")
	if err != nil || got.Old == nil || got.Old.Path != "b.txt" || !got.New.Present {
		t.Fatalf("cache miss diff = %+v, %v", got, err)
	}
	if _, err := repo.CommitFileDiff(ctx, commits[1], "missing.txt"); err == nil {
		t.Fatal("expected file outside commit to fail")
	}
}

func TestCompareFileDiffReusesComparison(t *testing.T) {
	repo, _, commits := cacheFixture(t)
	reset, calls := gitCallLog(t)
	ctx := context.Background()
	comparison, err := repo.Compare(ctx, commits[0], commits[2], false)
	if err != nil || len(comparison.Files) == 0 {
		t.Fatal(comparison, err)
	}
	reset()
	for _, file := range comparison.Files {
		got, err := repo.CompareFileDiff(ctx, comparison.From, comparison.Target, file.Path)
		if err != nil {
			t.Fatal(file.Path, err)
		}
		if calls("--name-status") != 0 {
			t.Fatalf("%s re-read comparison files", file.Path)
		}
		want, err := (&Repository{Path: repo.Path}).CompareFileDiff(ctx, comparison.From, comparison.Target, file.Path)
		if err != nil {
			t.Fatal(file.Path, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\n got %+v\nwant %+v", file.Path, got, want)
		}
		reset()
	}
	// 不同的比较两端不能命中上一次比较的列表。
	if _, err := repo.CompareFileDiff(ctx, commits[1], commits[2], "c.txt"); err == nil {
		t.Fatal("expected unchanged file outside comparison to fail")
	}
}

func TestDiffAndPatchShareBlobReads(t *testing.T) {
	repo, dir := fixture(t)
	write(t, dir, "work.txt", "one\ntwo\n")
	write(t, dir, "staged.txt", "alpha\nbeta\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	write(t, dir, "work.txt", "one\nchanged\n")
	write(t, dir, "staged.txt", "alpha\nstaged\n")
	git(t, dir, "add", "staged.txt")
	reset, calls := gitCallLog(t)
	ctx := context.Background()
	for _, test := range []struct {
		path   string
		staged bool
		blobs  int
	}{
		// 工作区改动：HEAD 与索引是同一 blob；暂存改动：HEAD 与索引各一个 blob。
		{path: "work.txt", blobs: 1},
		{path: "staged.txt", staged: true, blobs: 2},
	} {
		repo.blobs = nil
		reset()
		diff, err := repo.Diff(ctx, test.path, test.staged)
		if err != nil {
			t.Fatal(test.path, err)
		}
		patch, err := repo.Patch(ctx, test.path, test.staged)
		if err != nil || !patch.Supported {
			t.Fatal(test.path, patch, err)
		}
		if got := calls("blob"); got != test.blobs {
			t.Fatalf("%s blob reads = %d, want %d", test.path, got, test.blobs)
		}
		fresh := &Repository{Path: repo.Path}
		wantDiff, err := fresh.Diff(ctx, test.path, test.staged)
		if err != nil {
			t.Fatal(test.path, err)
		}
		wantPatch, err := (&Repository{Path: repo.Path}).Patch(ctx, test.path, test.staged)
		if err != nil {
			t.Fatal(test.path, err)
		}
		if !reflect.DeepEqual(diff, wantDiff) || !reflect.DeepEqual(patch, wantPatch) {
			t.Fatalf("%s:\n got %+v %+v\nwant %+v %+v", test.path, diff, patch, wantDiff, wantPatch)
		}
	}
}

func TestBlobCacheIsBounded(t *testing.T) {
	repo := &Repository{}
	repo.rememberBlob("large", strings.Repeat("x", fileContentLimit+1))
	if _, ok := repo.cachedBlob("large"); ok {
		t.Fatal("blob over fileContentLimit should not be cached")
	}
	for index := range blobCacheEntries + 1 {
		repo.rememberBlob(string(rune('a'+index)), "content")
	}
	if len(repo.blobs) != blobCacheEntries {
		t.Fatalf("cache entries = %d, want %d", len(repo.blobs), blobCacheEntries)
	}
	if _, ok := repo.cachedBlob("a"); ok {
		t.Fatal("oldest blob should be evicted")
	}
}
