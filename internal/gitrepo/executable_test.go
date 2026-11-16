// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWindowsBundledGitWithoutSystemPath(t *testing.T) {
	resources := t.TempDir()
	for _, directory := range []string{"cmd", "ucrt64/bin", "usr/bin"} {
		if err := os.MkdirAll(filepath.Join(resources, "git", directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(resources, "git", "cmd", "git.exe")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	options := ExecutableOptions{Resources: resources}
	actual, err := resolveExecutable(options, "windows")
	if err != nil || actual != path {
		t.Fatalf("Windows 默认没有使用内置 Git：%s %v", actual, err)
	}
	options.Source = "system"
	if _, err := resolveExecutable(options, "windows"); err == nil {
		t.Fatal("系统 Git 不存在时静默改用了内置 Git")
	}
	for _, executable := range []string{path, filepath.Join(resources, "git", "ucrt64", "bin", "git.exe")} {
		directories := executableDirectories(executable, "windows")
		for _, directory := range []string{"ucrt64/bin", "usr/bin"} {
			if !slices.Contains(directories, filepath.Join(resources, "git", directory)) {
				t.Fatalf("缺少辅助程序目录 %s：%v", directory, directories)
			}
		}
	}
}

func TestExecutableMissingAndInvalidSelection(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, options := range []ExecutableOptions{
		{Source: "system"}, {Source: "custom", Path: "git"}, {Source: "unknown"},
		{Source: "custom", Path: t.TempDir()}, {Source: "bundled", Resources: t.TempDir()},
	} {
		if _, err := ResolveExecutable(options); err == nil {
			t.Fatalf("接受了不可用的 Git：%+v", options)
		}
	}
}

func TestExecutableSelectionReachesEveryCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("使用 POSIX 包装器记录实际执行路径")
	}
	repo, root := fixture(t)
	git(t, root, "commit", "--allow-empty", "-m", "initial")
	actual, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "Git with spaces")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "git")
	log := filepath.Join(directory, "calls")
	t.Setenv("CODE_TEST_REAL_GIT", actual)
	t.Setenv("CODE_TEST_GIT_LOG", log)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CODE_TEST_GIT_LOG\"\nexec \"$CODE_TEST_REAL_GIT\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	previous := currentExecutableOptions()
	t.Cleanup(func() { ConfigureExecutable(previous) })
	ConfigureExecutable(ExecutableOptions{Source: "custom", Path: path})
	info, err := InspectExecutable(context.Background(), currentExecutableOptions())
	if err != nil || info.Path != path {
		t.Fatalf("自定义 Git 检测失败：%+v %v", info, err)
	}
	if _, err := run(context.Background(), root, "", time.Second*10, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	if err := runDetached(root, "rev-parse", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SearchHistory(context.Background(), "", "", 0, HistoryFilter{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"--version", "status --porcelain", "rev-parse HEAD", " log "} {
		if !strings.Contains(string(data), command) {
			t.Fatalf("未使用指定 Git 执行 %q：%s", command, data)
		}
	}
	t.Setenv("GIT_EXEC_PATH", "/invalid/inherited/git")
	command, err := gitCommand(context.Background(), "--version")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range command.Env {
		if strings.HasPrefix(entry, "GIT_EXEC_PATH=") {
			t.Fatal("保留了会覆盖所选 Git 辅助程序的环境变量")
		}
		if strings.HasPrefix(entry, "PATH="+directory+string(os.PathListSeparator)) {
			found = true
		}
	}
	if !found {
		t.Fatal("子进程没有优先使用所选 Git")
	}
}

func TestInspectExecutableRejectsNonGitAndCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("使用 POSIX 脚本模拟无效程序")
	}
	path := filepath.Join(t.TempDir(), "not-git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho hello\n"), 0700); err != nil {
		t.Fatal(err)
	}
	options := ExecutableOptions{Source: "custom", Path: path}
	if _, err := InspectExecutable(context.Background(), options); err == nil {
		t.Fatal("接受了非 Git 程序")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectExecutable(ctx, options); err == nil {
		t.Fatal("忽略了取消")
	}
}
