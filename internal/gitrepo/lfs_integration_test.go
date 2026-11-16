// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func realLFSTool(t *testing.T) {
	t.Helper()
	path := os.Getenv("XSOURCETREE_TEST_LFS")
	if path == "" {
		t.Skip("设置 XSOURCETREE_TEST_LFS 可使用独立 Git LFS 程序验证真实转换")
	}
	previousGit, previousLFS := executableOptions.Load(), lfsExecutableOptions.Load()
	ConfigureExecutable(ExecutableOptions{Source: "system"})
	ConfigureLFSExecutable(ExecutableOptions{Source: "custom", Path: path})
	t.Cleanup(func() {
		executableOptions.Store(previousGit)
		lfsExecutableOptions.Store(previousLFS)
	})
	t.Setenv("PATH", filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if _, err := InspectLFSExecutable(context.Background(), currentLFSExecutableOptions()); err != nil {
		t.Fatal(err)
	}
}

func TestRealLFSStagePreservesDraftsAndUntracksLiteralFile(t *testing.T) {
	realLFSTool(t)
	repo, root := fixture(t)
	ctx := context.Background()
	write(t, root, ".gitattributes", "# base\n")
	write(t, root, "other.txt", "base\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "base")
	if err := repo.InitializeLFS(ctx); err != nil {
		t.Fatal(err)
	}
	write(t, root, ".gitattributes", "# base\n# staged\n")
	git(t, root, "add", ".gitattributes")
	write(t, root, ".gitattributes", "# base\n# staged\n# unstaged\n")
	write(t, root, "other.txt", "staged\n")
	git(t, root, "add", "other.txt")
	write(t, root, "other.txt", "unstaged\n")
	payload := "\x00" + strings.Repeat("real payload", 4096)
	name := portableTestPath("目录/space [1]*.bin", "目录/space [1].bin")
	write(t, root, name, payload)
	beforeOther := git(t, root, "rev-parse", ":other.txt")
	if err := repo.StageWithLFS(ctx, []string{name}); err != nil {
		t.Fatal(err)
	}
	pointer, err := repo.git(ctx, "show", ":"+name)
	if err != nil || !isLFSPointer([]byte(pointer)) {
		t.Fatalf("真实过滤器没有生成有效指针：%q %v", pointer, err)
	}
	attributes := git(t, root, "show", ":.gitattributes")
	if !strings.Contains(attributes, "# staged") || strings.Contains(attributes, "# unstaged") {
		t.Fatalf("混入属性草稿或丢失已暂存规则：%s", attributes)
	}
	content, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil || !strings.Contains(string(content), "# unstaged") {
		t.Fatal("丢失工作区属性草稿：", err)
	}
	if git(t, root, "rev-parse", ":other.txt") != beforeOther {
		t.Fatal("改变了无关文件的部分暂存")
	}
	status, err := repo.LFSStatus(ctx)
	if err != nil || !status.Installed || len(status.Files) != 1 || status.Files[0].Path != name || !status.Files[0].Downloaded {
		t.Fatalf("真实 LFS JSON 状态错误：%+v %v", status, err)
	}
	if err := repo.UntrackLFS(ctx, []string{name}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Stage(ctx, []string{name}); err != nil {
		t.Fatal(err)
	}
	ordinary := git(t, root, "show", ":"+name)
	if ordinary != payload {
		t.Fatal("精确取消跟踪后仍按 LFS 指针暂存")
	}
}

func TestRealLFSPushAndDownloadLocalMissingObjects(t *testing.T) {
	realLFSTool(t)
	repo, root := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := repo.InitializeLFS(ctx); err != nil {
		t.Fatal(err)
	}
	payload := "\x00" + strings.Repeat("transfer payload", 1024)
	write(t, root, "large.bin", payload)
	if err := repo.StageWithLFS(ctx, []string{"large.bin"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Commit(ctx, "LFS commit"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateBranch(ctx, "without-lfs"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "large.bin")); err != nil {
		t.Fatal(err)
	}
	if err := repo.Stage(ctx, []string{"large.bin"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Commit(ctx, "remove file on another branch"); err != nil {
		t.Fatal(err)
	}
	if err := repo.SwitchBranch(ctx, "main"); err != nil {
		t.Fatal("普通分支切换没有通过 LFS 恢复文件：", err)
	}
	restored, err := os.ReadFile(filepath.Join(root, "large.bin"))
	if err != nil || string(restored) != payload {
		t.Fatalf("切换分支后文件内容错误：%v", err)
	}
	parent := t.TempDir()
	remote, clone := filepath.Join(parent, "remote.git"), filepath.Join(parent, "clone")
	git(t, parent, "init", "--bare", "-b", "main", remote)
	git(t, root, "remote", "add", "origin", remote)
	git(t, root, "config", "branch.main.remote", "origin")
	git(t, root, "config", "branch.main.merge", "refs/heads/main")
	if err := repo.Remote(ctx, "push"); err != nil {
		t.Fatal("LFS pre-push 未随普通推送执行：", err)
	}
	t.Setenv("GIT_LFS_SKIP_SMUDGE", "1")
	if _, err := run(ctx, parent, "", time.Minute, "clone", "--", remote, clone); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_LFS_SKIP_SMUDGE", "")
	copyRepo, err := Open(ctx, clone)
	if err != nil {
		t.Fatal(err)
	}
	if err := copyRepo.InitializeLFS(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := copyRepo.LFSStatus(ctx)
	if err != nil || len(status.Files) != 1 || status.Files[0].Downloaded {
		t.Fatalf("没有识别缺失内容：%+v %v", status, err)
	}
	if err := copyRepo.PullLFS(ctx); err != nil {
		t.Fatal("本地 LFS 下载失败：", err)
	}
	content, err := os.ReadFile(filepath.Join(clone, "large.bin"))
	if err != nil || string(content) != payload {
		t.Fatalf("没有还原真实文件内容：%v", err)
	}
	if err := copyRepo.Remote(ctx, "fetch"); err != nil {
		t.Fatal(err)
	}
	if err := copyRepo.Remote(ctx, "pull"); err != nil {
		t.Fatal(err)
	}
}
