// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNulRecordsKeepsPrefixesAcrossChunkBoundaries(t *testing.T) {
	type record struct {
		prefix string
		length int
	}
	var got []record
	records := &nulRecords{handle: func(prefix []byte, length int) error {
		got = append(got, record{string(prefix), length})
		return nil
	}}
	data := "?? a\x00\x00H very/long/path\x00S x\x00tail"
	// 逐字节写入，覆盖记录和前缀被任意切分的情况。
	for index := range len(data) {
		if written, err := records.Write([]byte{data[index]}); written != 1 || err != nil {
			t.Fatalf("Write 返回 %d %v", written, err)
		}
	}
	if err := records.finish(); err != nil {
		t.Fatal(err)
	}
	expected := []record{{"?? a", 4}, {"", 0}, {"H ve", 16}, {"S x", 3}, {"tail", 4}}
	if fmt.Sprint(got) != fmt.Sprint(expected) {
		t.Fatalf("记录切分错误：%v，预期 %v", got, expected)
	}
	failing := &nulRecords{handle: func([]byte, int) error { return errors.New("bad") }}
	// 解析失败后仍须吞下全部输出，避免子进程阻塞在满管道上。
	if written, err := failing.Write([]byte("a\x00b\x00")); written != 4 || err != nil {
		t.Fatalf("解析错误不应中断写入：%d %v", written, err)
	}
	if err := failing.finish(); err == nil || err.Error() != "bad" {
		t.Fatalf("没有保留首个解析错误：%v", err)
	}
}

func TestWorktreeInspectionStreamsStatusBeyondOutputLimit(t *testing.T) {
	repo, dir, path := worktreeFixture(t)
	git(t, dir, "config", "--local", "core.longpaths", "true")
	// 每条记录约 800 字节（不超过 macOS 的 PATH_MAX），约 5300 个未跟踪文件即超过 4 MiB 的输出上限。
	directory := filepath.Join(path, strings.Repeat("d", 200), strings.Repeat("e", 200), strings.Repeat("f", 200))
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for index := range 6000 {
		name := fmt.Sprintf("%05d-%s", index, strings.Repeat("x", 150))
		if err := os.WriteFile(filepath.Join(directory, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	review := worktreeReview(t, repo, path)
	if review.Worktree.Problem != "" || !review.Worktree.Dirty || review.CanRemove {
		t.Fatalf("超大状态输出没有被正确解析：%+v", review)
	}
	worktrees, err := repo.Worktrees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, worktree := range worktrees {
		if worktree.Problem != "" {
			t.Fatalf("超大状态输出被误报为问题：%+v", worktree)
		}
	}
}

// loggingGit 用记录参数的包装脚本代替 Git，返回日志路径。
func loggingGit(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("此测试使用 POSIX 包装脚本")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitPath := filepath.Join(t.TempDir(), "git")
	log := filepath.Join(t.TempDir(), "git-calls")
	t.Setenv("CODE_TEST_REAL_GIT", realGit)
	t.Setenv("CODE_TEST_GIT_LOG", log)
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CODE_TEST_GIT_LOG\"\nexec \"$CODE_TEST_REAL_GIT\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	previous := currentExecutableOptions()
	t.Cleanup(func() { ConfigureExecutable(previous) })
	ConfigureExecutable(ExecutableOptions{Source: "custom", Path: gitPath})
	return log
}

func countGitCalls(t *testing.T, log, fragment string) int {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), fragment)
}

func TestWorktreeReviewsInspectOnlyRelatedWorktrees(t *testing.T) {
	repo, dir, path := worktreeFixture(t)
	parent := filepath.Dir(path)
	for _, name := range []string{"other-a", "other-b"} {
		git(t, dir, "worktree", "add", "-b", name, filepath.Join(parent, name), "HEAD")
	}
	git(t, dir, "branch", "plain")
	log := loggingGit(t)
	ctx := context.Background()
	const status = "status --porcelain=v1"

	worktrees, err := repo.Worktrees(ctx)
	if err != nil || len(worktrees) != 4 {
		t.Fatalf("列表应完整检查全部工作树：%+v %v", worktrees, err)
	}
	if calls := countGitCalls(t, log, status); calls != 4 {
		t.Fatalf("列表检查了 %d 个工作树，预期 4", calls)
	}
	review := worktreeReview(t, repo, path)
	if review.Worktree.Problem != "" || !review.CanRemove {
		t.Fatalf("审查结果错误：%+v", review)
	}
	if calls := countGitCalls(t, log, status) - 4; calls != 2 {
		t.Fatalf("审查检查了 %d 个工作树，预期只检查源和目标 2 个", calls)
	}
	before := countGitCalls(t, log, status)
	deletion, err := repo.ReviewBranchDeletion(ctx, "plain", false)
	if err != nil || !deletion.CanDelete {
		t.Fatalf("普通分支删除审查失败：%+v %v", deletion, err)
	}
	if calls := countGitCalls(t, log, status) - before; calls != 1 {
		t.Fatalf("删除未检出的分支检查了 %d 个工作树，预期只检查当前工作树", calls)
	}
	// 范围缩小后执行前的最终复核仍须拦截源工作树的新改动。
	write(t, path, "late.txt", "late\n")
	if err := repo.RemoveWorktree(ctx, path, review.Token); err == nil {
		t.Fatal("复核没有发现审查后新增的未跟踪文件")
	}
	if _, err := os.Stat(filepath.Join(path, "late.txt")); err != nil {
		t.Fatal("复核失败后丢失了工作树数据：", err)
	}
}

func TestLFSStatusStreamsListBeyondOutputLimit(t *testing.T) {
	fakeLFS(t)
	repo, _ := fixture(t)
	var document strings.Builder
	document.WriteString(`{"files":[`)
	const count = 30000
	for index := range count {
		if index > 0 {
			document.WriteByte(',')
		}
		fmt.Fprintf(&document, `{"name":"assets/%06d-%s.bin","size":7,"checkout":true,"downloaded":%t,"oid_type":"sha256","oid":"%064d","version":"https://git-lfs.github.com/spec/v1"}`,
			index, strings.Repeat("n", 40), index%2 == 0, index)
	}
	document.WriteString(`],"extra":{"ignored":[1,2]}}`)
	if document.Len() <= outputLimit {
		t.Fatalf("测试数据没有超过输出上限：%d", document.Len())
	}
	path := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(path, []byte(document.String()), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_TEST_LFS_JSON", path)
	status, err := repo.LFSStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Files) != count || !status.Files[0].Downloaded || status.Files[1].Downloaded ||
		status.Files[count-1].Path != fmt.Sprintf("assets/%06d-%s.bin", count-1, strings.Repeat("n", 40)) {
		t.Fatalf("流式解析丢失条目：%d", len(status.Files))
	}
	files, truncated, err := decodeLFSFiles(strings.NewReader(document.String()), 10)
	if err != nil || !truncated || len(files) != 10 {
		t.Fatalf("条目上限无效：%d %v %v", len(files), truncated, err)
	}
	for _, invalid := range []string{`{"files":[{"name":"a.bin"}`, `[]`, `{"files":{}}`} {
		if _, _, err := decodeLFSFiles(strings.NewReader(invalid), 10); err == nil {
			t.Fatalf("接受了不完整或格式错误的 JSON：%s", invalid)
		}
	}
	if files, _, err := decodeLFSFiles(strings.NewReader(`{"files":null}`), 10); err != nil || len(files) != 0 {
		t.Fatalf("无文件时应返回空列表：%v %v", files, err)
	}
}

func TestLFSStageCleansTrackedAndUntrackedFilesOnce(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, "tracked.bin", "old")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "base")
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	write(t, root, "tracked.bin", "payload")
	write(t, root, "new.bin", "payload")
	// 刚写入的文件与索引同一时刻修改，Git 写索引时会重新哈希这类 racily-clean 条目并再次调用 clean；
	// 真实场景中待转换的大文件通常早已存在，把时间调到过去以只统计本流程主动触发的清洗。
	past := time.Now().Add(-time.Hour)
	for _, name := range []string{"tracked.bin", "new.bin"} {
		if err := os.Chtimes(filepath.Join(root, name), past, past); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("CODE_TEST_LFS_LOG", log)
	if err := repo.StageWithLFS(context.Background(), []string{"tracked.bin", "new.bin"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tracked.bin", "new.bin"} {
		if pointer := git(t, root, "show", ":"+name); !validLFSPointer(pointer) {
			t.Fatalf("%s 没有暂存为 LFS 指针：%q", name, pointer)
		}
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if calls := strings.Count(string(data), "|clean -- "+name+"\n"); calls != 1 {
			t.Fatalf("%s 经过 clean filter %d 次，预期 1 次\n%s", name, calls, data)
		}
	}
	// 不用 status：测试辅助的 git 会调用系统中的 git-lfs 重新清洗工作区文件。
	if status := strings.TrimSpace(git(t, root, "diff", "--cached", "--name-status")); status != "A\t.gitattributes\nA\tnew.bin\nM\ttracked.bin" {
		t.Fatalf("暂存结果错误：%q", status)
	}
}

func TestStashAndDiscardLockWaitCanBeCancelled(t *testing.T) {
	repo, root := fixture(t)
	write(t, root, "file.txt", "base\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "base")
	write(t, root, "file.txt", "changed\n")
	// 模拟长时间 fetch/pull 持有仓库锁。
	repo.mu.Lock()
	defer repo.mu.Unlock()
	operations := map[string]func(context.Context) error{
		"stash": func(ctx context.Context) error { return repo.CreateStash(ctx, "", false) },
		"stashes": func(ctx context.Context) error {
			_, err := repo.Stashes(ctx)
			return err
		},
		"discard preview": func(ctx context.Context) error {
			_, err := repo.DiscardPreview(ctx, "file.txt")
			return err
		},
		"discard": func(ctx context.Context) error {
			_, err := repo.Discard(ctx, "file.txt", "token", t.TempDir())
			return err
		},
	}
	for name, operation := range operations {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		started := time.Now()
		err := operation(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 5*time.Second {
			t.Fatalf("%s 等待仓库锁时不能取消：%v", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "file.txt")); err != nil || string(data) != "changed\n" {
		t.Fatalf("取消后修改了工作区：%q %v", data, err)
	}
}
