// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// gitCallLog 记录被测代码启动的 Git 子命令，用于确认缓存确实省去了进程。
func gitCallLog(t *testing.T) (reset func(), calls func(command string) int) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("XSOURCE_GIT_LOG", log)
	installGitTestWrapper(t, "git-call-log")
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
		// 新实例各用一次 log 同时取得文件列表与父提交；复用实例不再启动任何读取列表或父提交的进程。
		if got, want := calls("log"), len(files); got != want {
			t.Fatalf("%s log calls = %d, want %d", hash, got, want)
		}
		if got := calls("rev-list") + calls("diff-tree") + calls("cat-file -t"); got != 0 {
			t.Fatalf("%s extra commit reads = %d, want 0", hash, got)
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
		repo.forgetBlobs()
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
	t.Cleanup(repo.forgetBlobs)
	repo.rememberBlob("large", strings.Repeat("x", fileContentLimit+1))
	if _, ok := repo.cachedBlob("large"); ok {
		t.Fatal("blob over fileContentLimit should not be cached")
	}
	for index := range blobCacheEntries + 1 {
		repo.rememberBlob(string(rune('a'+index)), "content")
	}
	if got := ownedBlobs(repo); got != blobCacheEntries {
		t.Fatalf("cache entries = %d, want %d", got, blobCacheEntries)
	}
	if _, ok := repo.cachedBlob("a"); ok {
		t.Fatal("oldest blob should be evicted")
	}
}

// 多个仓库共享总量上限：切换到其他仓库后，先前仓库的大 blob 会被淘汰而不是一直保留。
func TestBlobCacheIsSharedAcrossRepositories(t *testing.T) {
	first, second := &Repository{}, &Repository{}
	t.Cleanup(first.forgetBlobs)
	t.Cleanup(second.forgetBlobs)
	large := strings.Repeat("x", fileContentLimit)
	for index := range blobCacheBytes / fileContentLimit {
		first.rememberBlob(string(rune('a'+index)), large)
	}
	if _, ok := first.cachedBlob("a"); !ok {
		t.Fatal("first repository blob should be cached")
	}
	// 命中会刷新最近使用顺序，"a" 不应先于 "b" 被淘汰。
	second.rememberBlob("a", large)
	if _, ok := first.cachedBlob("a"); !ok {
		t.Fatal("recently used blob should stay cached")
	}
	if _, ok := first.cachedBlob("b"); ok {
		t.Fatal("least recently used blob should be evicted")
	}
	if content, ok := second.cachedBlob("a"); !ok || content != large {
		t.Fatal("repositories must not share entries by hash")
	}
	blobCache.Lock()
	total := blobCache.bytes
	blobCache.Unlock()
	if total > blobCacheBytes {
		t.Fatalf("cache bytes = %d, want <= %d", total, blobCacheBytes)
	}
}

func ownedBlobs(repo *Repository) int {
	blobCache.Lock()
	defer blobCache.Unlock()
	count := 0
	for _, blob := range blobCache.entries {
		if blob.owner == repo {
			count++
		}
	}
	return count
}

// forgetBlobs 丢弃该仓库实例的全部 blob 缓存。
func (repo *Repository) forgetBlobs() {
	blobCache.Lock()
	defer blobCache.Unlock()
	blobCache.entries = slices.DeleteFunc(blobCache.entries, func(blob cachedBlob) bool {
		if blob.owner == repo {
			blobCache.bytes -= len(blob.content)
			return true
		}
		return false
	})
}
