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

func fakeLFS(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("此测试使用 POSIX 假程序；Windows 来源解析另有覆盖")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	path := filepath.Join(t.TempDir(), "git-lfs")
	script := `#!/bin/sh
if [ -n "$CODE_TEST_LFS_LOG" ]; then printf '%s|%s\n' "$PWD" "$*" >> "$CODE_TEST_LFS_LOG"; fi
case "$1" in
version)
  if [ -n "$CODE_TEST_LFS_VERSION_MUTATE" ]; then printf changed > "$CODE_TEST_LFS_VERSION_MUTATE"; fi
  printf 'git-lfs/3.7.0 (test)\n' ;;
ls-files)
  [ "$2" = "--json" ] || exit 90
  if [ -n "$CODE_TEST_LFS_JSON" ]; then cat "$CODE_TEST_LFS_JSON"; exit; fi
  printf '{"files":[{"name":"space [1].bin","size":7,"checkout":true,"downloaded":true,"oid_type":"sha256","oid":"0000000000000000000000000000000000000000000000000000000000000000","version":"https://git-lfs.github.com/spec/v1"},{"name":"line\\nbreak.bin","size":7,"checkout":false,"downloaded":false,"oid_type":"sha256","oid":"1111111111111111111111111111111111111111111111111111111111111111","version":"https://git-lfs.github.com/spec/v1"}]}\n' ;;
install)
  [ "$2" = "--local" ] && [ "$3" = "--skip-repo" ] && [ "$#" = 3 ] || exit 91
  git config --local filter.lfs.clean 'git-lfs clean -- %f' || exit
  git config --local filter.lfs.smudge 'git-lfs smudge -- %f' || exit
  git config --local filter.lfs.required true ;;
clean)
  cat >/dev/null
  [ "$CODE_TEST_LFS_FAIL" != 1 ] || exit 92
  if [ -n "$CODE_TEST_LFS_MUTATE" ]; then printf changed > "$CODE_TEST_LFS_MUTATE"; fi
  if [ -n "$CODE_TEST_LFS_FLAG_INDEX" ]; then GIT_INDEX_FILE="$CODE_TEST_LFS_FLAG_INDEX" git update-index --assume-unchanged -- .gitattributes || exit; fi
  if [ "$CODE_TEST_LFS_INVALID_POINTER" = 1 ]; then printf 'version https://git-lfs.github.com/spec/v1\nraw bytes\n'; exit 0; fi
  printf 'version https://git-lfs.github.com/spec/v1\noid sha256:0000000000000000000000000000000000000000000000000000000000000000\nsize 7\n' ;;
smudge) cat ;;
pull) [ "$#" = 1 ] || exit 93 ;;
pre-push|post-checkout|post-commit|post-merge) exit 0 ;;
*) exit 94 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var err error
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	previous := lfsExecutableOptions.Load()
	t.Cleanup(func() { lfsExecutableOptions.Store(previous) })
	ConfigureLFSExecutable(ExecutableOptions{Source: "custom", Path: path})
	return path
}

func TestLFSExecutableSourcesDoNotFallback(t *testing.T) {
	resources := t.TempDir()
	path := filepath.Join(resources, "git", "cmd", "git-lfs.exe")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if actual, err := resolveLFSExecutable(ExecutableOptions{Resources: resources}, "windows"); err != nil || actual != path {
		t.Fatalf("没有复用 Windows 内置 Git LFS：%q %v", actual, err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, options := range []ExecutableOptions{
		{Source: "system"}, {Source: "custom", Path: "git-lfs"},
		{Source: "custom", Path: filepath.Join(resources, "renamed-lfs")},
		{Source: "bundled", Resources: t.TempDir()}, {Source: "unknown"},
	} {
		if _, err := resolveLFSExecutable(options, "windows"); err == nil {
			t.Fatalf("失效配置静默回退：%+v", options)
		}
	}
}

func TestLFSExecutableRejectsRenamedAndNonExecutableTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 使用扩展名判断可执行文件")
	}
	for _, entry := range []struct {
		name string
		mode os.FileMode
	}{{"renamed-lfs", 0700}, {"git-lfs", 0600}} {
		path := filepath.Join(t.TempDir(), entry.name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), entry.mode); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveLFSExecutable(ExecutableOptions{Source: "custom", Path: path}); err == nil {
			t.Fatal("接受了 hooks 无法调用的程序：", path)
		}
	}
}

func TestLFSInspectionAndGitChildrenUseSelectedTools(t *testing.T) {
	path := fakeLFS(t)
	repo, root := fixture(t)
	info, err := InspectLFSExecutable(context.Background(), ExecutableOptions{Source: "custom", Path: path})
	if err != nil || info.Path != path || !strings.HasPrefix(info.Version, "git-lfs/") {
		t.Fatalf("工具检测失败：%+v %v", info, err)
	}
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("CODE_TEST_LFS_LOG", log)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitPath := filepath.Join(t.TempDir(), "git")
	t.Setenv("CODE_TEST_REAL_GIT", realGit)
	t.Setenv("CODE_TEST_GIT_LOG", filepath.Join(t.TempDir(), "git-calls"))
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CODE_TEST_GIT_LOG\"\nexec \"$CODE_TEST_REAL_GIT\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(gitPath), "git-lfs"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	previous := currentExecutableOptions()
	t.Cleanup(func() { ConfigureExecutable(previous) })
	ConfigureExecutable(ExecutableOptions{Source: "custom", Path: gitPath})
	t.Setenv("GIT_DIR", "/invalid/inherited")
	t.Setenv("GIT_WORK_TREE", "/invalid/inherited")
	t.Setenv("GIT_EXEC_PATH", "/invalid/inherited")
	command, err := gitCommand(context.Background(), "--exec-path")
	if err != nil {
		t.Fatal(err)
	}
	foundPath := false
	for _, entry := range command.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(key, "GIT_EXEC_PATH") {
			t.Fatal("LFS 选择覆盖了 Git 的标准辅助程序目录")
		}
		if strings.EqualFold(key, "PATH") {
			foundPath = strings.HasPrefix(value, filepath.Dir(path)+string(os.PathListSeparator))
		}
	}
	if !foundPath {
		t.Fatal("所选 LFS 目录没有优先于 Git 目录中的其他 LFS")
	}
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	write(t, root, "asset.bin", "payload")
	if err := repo.StageWithLFS(context.Background(), []string{"asset.bin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.git(context.Background(), "hook", "run", "pre-push"); err != nil {
		t.Fatal("hook 没有使用所选 Git LFS：", err)
	}
	output, err := repo.git(context.Background(), "show", ":asset.bin")
	if err != nil || !strings.HasPrefix(output, "version https://git-lfs.github.com/spec/v1\n") {
		t.Fatalf("Git filter 未使用所选 LFS：%q %v", output, err)
	}
	data, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(data), repo.Path+"|install --local --skip-repo") ||
		!strings.Contains(string(data), "|clean -- asset.bin") || !strings.Contains(string(data), "|pre-push") {
		t.Fatalf("LFS 未在仓库中直接执行：%s %v", data, err)
	}
	data, err = os.ReadFile(os.Getenv("CODE_TEST_GIT_LOG"))
	if err != nil || !strings.Contains(string(data), "config --local filter.lfs.clean") {
		t.Fatalf("LFS 子进程未使用所选 Git：%s %v", data, err)
	}
}

func TestLFSInitializationPreservesHooksAndConfigOnConflict(t *testing.T) {
	fakeLFS(t)
	for _, name := range []string{"pre-push", "post-checkout", "post-commit", "post-merge"} {
		t.Run(name, func(t *testing.T) {
			repo, root := fixture(t)
			git(t, root, "config", "core.hooksPath", "custom-hooks")
			write(t, root, "custom-hooks/"+name, "#!/bin/sh\nprintf custom\n")
			before, err := os.ReadFile(filepath.Join(root, ".git", "config"))
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.InitializeLFS(context.Background()); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("未报告具体 hook 冲突：%v", err)
			}
			after, err := os.ReadFile(filepath.Join(root, ".git", "config"))
			if err != nil || string(before) != string(after) {
				t.Fatal("hook 预检失败前已修改配置")
			}
			data, err := os.ReadFile(filepath.Join(root, "custom-hooks", name))
			if err != nil || string(data) != "#!/bin/sh\nprintf custom\n" {
				t.Fatal("覆盖了用户 hook")
			}
		})
	}
}

func TestLFSInitializationIsLocalAndPreservesConfiguredHooksPath(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	git(t, root, "config", "core.hooksPath", "custom-hooks")
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal("重复初始化不应重写或拒绝自己的 hook：", err)
	}
	if value := git(t, root, "config", "--local", "--get", "core.hooksPath"); value != "custom-hooks" {
		t.Fatal("修改了 core.hooksPath：", value)
	}
	for _, name := range []string{"pre-push", "post-checkout", "post-commit", "post-merge"} {
		if _, err := os.Stat(filepath.Join(root, "custom-hooks", name)); err != nil {
			t.Fatal(err)
		}
	}
	status, err := repo.LFSStatus(context.Background())
	if err != nil || !status.Installed || status.Problem != "" || status.HooksPath != filepath.Join(repo.Path, "custom-hooks") {
		t.Fatalf("仓库状态错误：%+v %v", status, err)
	}
	if len(status.Files) != 2 || status.Files[0].Path != "space [1].bin" || !status.Files[0].Downloaded || status.Files[1].Path != "line\nbreak.bin" || status.Files[1].Downloaded {
		t.Fatalf("JSON 文件状态解析错误：%+v", status.Files)
	}
}

func TestLFSInitializationDoesNotCreateInheritedGlobalHooks(t *testing.T) {
	for _, existing := range []int{0, 1, 4} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			fakeLFS(t)
			repo, root := fixture(t)
			shared := filepath.Join(t.TempDir(), "shared hooks")
			if err := os.MkdirAll(shared, 0700); err != nil {
				t.Fatal(err)
			}
			names := []string{"pre-push", "post-checkout", "post-commit", "post-merge"}
			contents := make(map[string]string)
			for _, name := range names[:existing] {
				content := "#!/bin/sh\ngit lfs " + name + " \"$@\"\n"
				if err := os.WriteFile(filepath.Join(shared, name), []byte(content), 0755); err != nil {
					t.Fatal(err)
				}
				contents[name] = content
			}
			global := filepath.Join(t.TempDir(), "global.gitconfig")
			git(t, root, "config", "--file", global, "core.hooksPath", shared)
			t.Setenv("GIT_CONFIG_GLOBAL", global)
			beforeLocal, err := os.ReadFile(filepath.Join(root, ".git", "config"))
			if err != nil {
				t.Fatal(err)
			}
			beforeGlobal, err := os.ReadFile(global)
			if err != nil {
				t.Fatal(err)
			}
			err = repo.InitializeLFS(context.Background())
			if existing < len(names) {
				if err == nil || !strings.Contains(err.Error(), "global") || !strings.Contains(err.Error(), "手动") {
					t.Fatalf("没有在配置变更前明确拒绝写入共享 hooks：%v", err)
				}
				after, err := os.ReadFile(filepath.Join(root, ".git", "config"))
				if err != nil || string(after) != string(beforeLocal) {
					t.Fatal("拒绝写入共享 hooks 时仍修改了本地配置")
				}
			} else {
				if err != nil {
					t.Fatal("共享 hooks 已完整兼容时应允许仅配置本地 filters：", err)
				}
				state, err := repo.LFSStatus(context.Background())
				if err != nil || !state.Installed || state.Problem != "" {
					t.Fatalf("未完成本地 filters 初始化：%+v %v", state, err)
				}
			}
			afterGlobal, err := os.ReadFile(global)
			if err != nil || string(afterGlobal) != string(beforeGlobal) {
				t.Fatal("修改了全局配置")
			}
			entries, err := os.ReadDir(shared)
			if err != nil || len(entries) != existing {
				t.Fatalf("向共享 hooks 目录写入了新文件：%v %v", entries, err)
			}
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(shared, entry.Name()))
				if err != nil || string(data) != contents[entry.Name()] {
					t.Fatal("修改了现有共享 hook：", entry.Name())
				}
			}
		})
	}
}

func TestLFSStagePreservesUnrelatedAttributesAndPartialStaging(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, ".gitattributes", "*.txt text\n")
	write(t, root, "unrelated.txt", "base\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "base")
	write(t, root, ".gitattributes", "*.txt text\n*.png -text\n")
	git(t, root, "add", ".gitattributes")
	write(t, root, ".gitattributes", "*.txt text\n*.png -text\n*.md text\n")
	write(t, root, "unrelated.txt", "staged\n")
	git(t, root, "add", "unrelated.txt")
	write(t, root, "unrelated.txt", "unstaged\n")
	write(t, root, "space [1].bin", "payload")
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := repo.StageWithLFS(context.Background(), []string{"space [1].bin"}); err != nil {
		t.Fatal(err)
	}
	staged := git(t, root, "show", ":.gitattributes")
	if !strings.HasPrefix(staged, "*.txt text\n*.png -text\n") || strings.Contains(staged, "*.md text") {
		t.Fatalf("混入未暂存属性或丢失已暂存属性：%q", staged)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil || !strings.HasPrefix(string(data), "*.txt text\n*.png -text\n*.md text\n") {
		t.Fatalf("丢失工作区属性：%q %v", data, err)
	}
	if value := git(t, root, "show", ":unrelated.txt"); value != "staged" {
		t.Fatal("修改了其他文件的部分暂存：", value)
	}
	value, err := repo.git(context.Background(), "check-attr", "filter", "--", "space [1].bin", "space 1.bin")
	if err != nil || !strings.Contains(value, "space [1].bin: filter: lfs") || !strings.Contains(value, "space 1.bin: filter: unspecified") {
		t.Fatalf("跟踪规则扩展为 glob：%q %v", value, err)
	}
}

func TestLFSStageRejectsPartlyStagedSelectionBeforeMutation(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, "asset.bin", "base")
	git(t, root, "add", "asset.bin")
	git(t, root, "commit", "-m", "base")
	write(t, root, "asset.bin", "staged")
	git(t, root, "add", "asset.bin")
	write(t, root, "asset.bin", "unstaged")
	before := git(t, root, "ls-files", "--stage")
	config, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.StageWithLFS(context.Background(), []string{"asset.bin"}); err == nil {
		t.Fatal("接受了部分暂存文件")
	}
	if _, err := os.Stat(filepath.Join(root, ".gitattributes")); !os.IsNotExist(err) {
		t.Fatal("预检失败仍创建了属性文件")
	}
	after, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	if err != nil || string(config) != string(after) || git(t, root, "ls-files", "--stage") != before {
		t.Fatal("预检失败修改了配置或索引")
	}
}

func TestLFSStageRollsBackAttributesAndIndexOnFilterFailure(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, ".gitattributes", "*.txt text\n")
	git(t, root, "add", ".gitattributes")
	write(t, root, ".gitattributes", "*.txt text\n# keep unstaged\n")
	write(t, root, "asset.bin", "payload")
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := git(t, root, "ls-files", "--stage")
	t.Setenv("CODE_TEST_LFS_FAIL", "1")
	if err := repo.StageWithLFS(context.Background(), []string{"asset.bin"}); err == nil {
		t.Fatal("忽略了 clean filter 失败")
	}
	if git(t, root, "ls-files", "--stage") != before {
		t.Fatal("失败后索引发生变化")
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil || string(data) != "*.txt text\n# keep unstaged\n" {
		t.Fatalf("失败后属性没有恢复：%q %v", data, err)
	}
}

func TestLFSLiteralTrackAndUntrack(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, ".gitattributes", "*.bin filter=lfs diff=lfs merge=lfs -text\n# keep\n")
	write(t, root, "[x].bin", "payload")
	write(t, root, "x.bin", "payload")
	if err := repo.TrackLFS(context.Background(), []string{"[x].bin"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UntrackLFS(context.Background(), []string{"[x].bin"}); err != nil {
		t.Fatal(err)
	}
	output, err := repo.git(context.Background(), "check-attr", "filter", "--", "[x].bin", "x.bin")
	if err != nil || !strings.Contains(output, "[x].bin: filter: unspecified") || !strings.Contains(output, "x.bin: filter: lfs") {
		t.Fatalf("取消跟踪影响了其他文件：%q %v", output, err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil || !strings.HasPrefix(string(data), "*.bin filter=lfs diff=lfs merge=lfs -text\n# keep\n") {
		t.Fatal("修改了用户已有属性")
	}
	if got := git(t, root, "diff", "--cached", "--name-only"); got != "" {
		t.Fatal("跟踪操作不应自动暂存：", got)
	}
}

func TestLFSRejectsUnsafeSelectionsAndMalformedAttributes(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, "asset.bin", "payload")
	for _, path := range []string{"", ".", "../escape", ".git/config", "a/../asset.bin", ".gitattributes", "dir/.gitattributes", "line\nbreak", "dir\\escape"} {
		if err := repo.StageWithLFS(context.Background(), []string{path}); err == nil {
			t.Fatalf("接受了不安全路径：%q", path)
		}
	}
	for _, content := range []string{"\"unterminated filter=lfs\n", "<<<<<<< conflict\n", "bad\x00attribute\n"} {
		write(t, root, ".gitattributes", content)
		if err := repo.StageWithLFS(context.Background(), []string{"asset.bin"}); err == nil {
			t.Fatalf("接受了损坏属性：%q", content)
		}
		data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
		if err != nil || string(data) != content {
			t.Fatal("覆盖了损坏属性文件")
		}
	}
}

func TestLFSStageRejectsSymlinkedAttributes(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	outside := filepath.Join(t.TempDir(), "attrs")
	if err := os.WriteFile(outside, []byte("# outside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".gitattributes")); err != nil {
		t.Skip("无法创建符号链接：", err)
	}
	write(t, root, "asset.bin", "payload")
	if err := repo.StageWithLFS(context.Background(), []string{"asset.bin"}); err == nil {
		t.Fatal("接受了属性符号链接")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "# outside\n" {
		t.Fatal("修改了仓库外文件")
	}
}

func TestLFSInspectionCancellationStopsDescendants(t *testing.T) {
	path := fakeLFS(t)
	marker := filepath.Join(t.TempDir(), "survived")
	t.Setenv("CODE_TEST_LFS_SURVIVED", marker)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n(sleep 1; printf survived > \"$CODE_TEST_LFS_SURVIVED\") &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := InspectLFSExecutable(ctx, ExecutableOptions{Source: "custom", Path: path})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("丢失取消原因：%v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("LFS 后代进程在取消后继续执行")
	}
}

func TestLFSStageRefusesUninitializedRepositoryWithoutChangingDrafts(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, ".gitattributes", "# user draft\n")
	write(t, root, "asset.bin", "payload")
	if err := repo.StageWithLFS(context.Background(), []string{"asset.bin"}); err == nil || !strings.Contains(err.Error(), "初始化") {
		t.Fatalf("未初始化仓库没有明确拒绝：%v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil || string(data) != "# user draft\n" || git(t, root, "ls-files") != "" {
		t.Fatal("拒绝操作后仍改变了草稿或索引")
	}
}

func TestLFSStageRejectsNonpointerFilterOutput(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, "asset.bin", "payload")
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_TEST_LFS_INVALID_POINTER", "1")
	if err := repo.StageWithLFS(context.Background(), []string{"asset.bin"}); err == nil {
		t.Fatal("仅包含 pointer 首行的普通内容被暂存")
	}
	if git(t, root, "ls-files") != "" {
		t.Fatal("无效指针写入了真实索引")
	}
	if _, err := os.Stat(filepath.Join(root, ".gitattributes")); !os.IsNotExist(err) {
		t.Fatal("无效指针留下了新增属性文件")
	}
}

func TestLFSStageRollsBackWhenRealIndexIsLocked(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, "asset.bin", "payload")
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("other process"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := repo.StageWithLFS(context.Background(), []string{"asset.bin"}); err == nil {
		t.Fatal("忽略真实索引锁")
	}
	data, err := os.ReadFile(lock)
	if err != nil || string(data) != "other process" {
		t.Fatal("触碰了其他进程的锁")
	}
	if _, err := os.Stat(filepath.Join(root, ".gitattributes")); !os.IsNotExist(err) {
		t.Fatal("索引写入失败没有回滚属性文件")
	}
	if got := git(t, root, "ls-files"); got != "" {
		t.Fatal("索引写入失败仍暂存了文件：", got)
	}
}

func TestLFSStatusReportsMissingSelectedTool(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	ConfigureLFSExecutable(ExecutableOptions{Source: "custom", Path: filepath.Join(t.TempDir(), "git-lfs")})
	state, err := repo.LFSStatus(context.Background())
	if err != nil || state.Installed || state.Problem == "" {
		t.Fatalf("没有报告所选工具不可用：%+v %v", state, err)
	}
	if _, err := gitCommand(context.Background(), "--version"); err != nil {
		t.Fatal("缺失可选 LFS 工具阻断了普通 Git 命令：", err)
	}
	if _, err := repo.Snapshot(context.Background()); err != nil {
		t.Fatal("缺失可选 LFS 工具阻断了仓库状态：", err)
	}
	if err := repo.PullLFS(context.Background()); err == nil {
		t.Fatal("显式 LFS 操作没有报告所选工具不可用")
	}
	write(t, root, "ordinary.txt", "normal Git operation\n")
	if err := repo.Stage(context.Background(), []string{"ordinary.txt"}); err != nil {
		t.Fatal("缺失可选 LFS 阻断了普通文件暂存：", err)
	}
	if err := repo.Commit(context.Background(), "ordinary file"); err != nil {
		t.Fatal("缺失可选 LFS 阻断了普通文件提交：", err)
	}
}

func TestLFSPointerValidationRejectsRawAndMalformedBytes(t *testing.T) {
	for _, content := range []string{
		"payload",
		"version https://git-lfs.github.com/spec/v1\n",
		"version https://git-lfs.github.com/spec/v1\noid sha256:invalid\nsize 7\n",
		"version https://git-lfs.github.com/spec/v1\noid sha256:0000000000000000000000000000000000000000000000000000000000000000\nsize -1\n",
		"version https://git-lfs.github.com/spec/v1\noid sha256:0000000000000000000000000000000000000000000000000000000000000000\nsize 7\nraw bytes\n",
	} {
		if validLFSPointer(content) {
			t.Fatalf("接受了非指针内容：%q", content)
		}
	}
	if !validLFSPointer("version https://git-lfs.github.com/spec/v1\noid sha256:0000000000000000000000000000000000000000000000000000000000000000\nsize 7\n") {
		t.Fatal("拒绝了有效指针")
	}
}

func TestLFSSystemSymlinkWorksWithSeparatelySelectedGit(t *testing.T) {
	path := fakeLFS(t)
	repo, _ := fixture(t)
	actualGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(t.TempDir(), "homebrew", "bin")
	if err := os.MkdirAll(prefix, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(prefix, "git-lfs")); err != nil {
		t.Skip("无法创建 Homebrew 风格符号链接：", err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "git"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", prefix+string(os.PathListSeparator)+os.Getenv("PATH"))
	previous := currentExecutableOptions()
	t.Cleanup(func() { ConfigureExecutable(previous) })
	ConfigureExecutable(ExecutableOptions{Source: "custom", Path: actualGit})
	ConfigureLFSExecutable(ExecutableOptions{Source: "system"})
	resolved, err := ResolveLFSExecutable(ExecutableOptions{Source: "system"})
	if err != nil || resolved != path {
		t.Fatalf("未解析到 LFS 独立安装目录：%q %v", resolved, err)
	}
	if _, err := InspectLFSExecutable(context.Background(), ExecutableOptions{Source: "system"}); err != nil {
		t.Fatal("错误拒绝了独立 Git 与 Homebrew LFS：", err)
	}
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal("LFS 子进程没有使用所选 Git：", err)
	}
}

func TestLFSInstalledReflectsRepositoryConfigurationIndependentlyOfTool(t *testing.T) {
	fakeLFS(t)
	repo, _ := fixture(t)
	state, err := repo.LFSStatus(context.Background())
	if err != nil || state.Installed || state.Problem == "" {
		t.Fatalf("只有工具就错误标记仓库已初始化：%+v %v", state, err)
	}
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	ConfigureLFSExecutable(ExecutableOptions{Source: "custom", Path: filepath.Join(t.TempDir(), "git-lfs")})
	state, err = repo.LFSStatus(context.Background())
	if err != nil || !state.Installed || state.Problem == "" {
		t.Fatalf("工具丢失错误改变了仓库初始化状态，或没有报告问题：%+v %v", state, err)
	}
}

func TestLFSStageInspectedAcceptsSubsetAndPreservesAttributeDraft(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, ".gitattributes", "*.txt text\n")
	git(t, root, "add", ".gitattributes")
	write(t, root, ".gitattributes", "*.txt text\n# user draft\n")
	write(t, root, "asset.bin", "payload")
	write(t, root, "ordinary.txt", "ordinary\n")
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	expected, err := repo.InspectFiles(context.Background(), []string{"asset.bin", "ordinary.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.StageWithLFSInspected(context.Background(), []string{"asset.bin"}, expected); err != nil {
		t.Fatal(err)
	}
	content, err := repo.git(context.Background(), "show", ":asset.bin")
	if err != nil || !validLFSPointer(content) {
		t.Fatalf("未生成已确认文件的 LFS 指针：%q %v", content, err)
	}
	if names := git(t, root, "ls-files"); strings.Contains(names, "ordinary.txt") {
		t.Fatal("暂存了确认报告中的未选文件：", names)
	}
	if staged := git(t, root, "show", ":.gitattributes"); strings.Contains(staged, "# user draft") {
		t.Fatal("混入未暂存属性草稿：", staged)
	}
}

func TestLFSStageInspectedRejectsChangedConfirmationBeforeAttributeMutation(t *testing.T) {
	for _, change := range []string{"worktree", "index", "tool-inspection", "missing-file", "missing-token", "duplicate-file"} {
		t.Run(change, func(t *testing.T) {
			fakeLFS(t)
			repo, root := fixture(t)
			write(t, root, ".gitattributes", "# user draft\n")
			write(t, root, "asset.bin", "payload")
			write(t, root, "ordinary.txt", "ordinary\n")
			if err := repo.InitializeLFS(context.Background()); err != nil {
				t.Fatal(err)
			}
			expected, err := repo.InspectFiles(context.Background(), []string{"asset.bin"})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "worktree":
				write(t, root, "asset.bin", "changed")
			case "index":
				git(t, root, "add", "ordinary.txt")
			case "tool-inspection":
				t.Setenv("CODE_TEST_LFS_VERSION_MUTATE", filepath.Join(root, "asset.bin"))
			case "missing-file":
				expected.Files = nil
			case "missing-token":
				expected.Token = ""
			case "duplicate-file":
				expected.Files = append(expected.Files, expected.Files[0])
			}
			before, err := runDigest(context.Background(), root, 30*time.Second, "ls-files", "--stage", "-z")
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.StageWithLFSInspected(context.Background(), []string{"asset.bin"}, expected); err == nil {
				t.Fatal("接受了确认后改变的文件或索引")
			}
			after, err := runDigest(context.Background(), root, 30*time.Second, "ls-files", "--stage", "-z")
			if err != nil || after != before {
				t.Fatal("拒绝转换仍修改了真实索引")
			}
			data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
			if err != nil || string(data) != "# user draft\n" {
				t.Fatalf("拒绝转换仍修改了属性草稿：%q %v", data, err)
			}
		})
	}
}

func TestLFSStageRejectsWorktreeMutationDuringCleanWithoutPublishingIndex(t *testing.T) {
	for _, inspected := range []bool{false, true} {
		t.Run(fmt.Sprint(inspected), func(t *testing.T) {
			fakeLFS(t)
			repo, root := fixture(t)
			write(t, root, ".gitattributes", "*.txt text\n")
			git(t, root, "add", ".gitattributes")
			write(t, root, ".gitattributes", "*.txt text\n# user draft\n")
			write(t, root, "asset.bin", "payload")
			if err := repo.InitializeLFS(context.Background()); err != nil {
				t.Fatal(err)
			}
			expected, err := repo.InspectFiles(context.Background(), []string{"asset.bin"})
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("CODE_TEST_LFS_MUTATE", filepath.Join(root, "asset.bin"))
			if inspected {
				err = repo.StageWithLFSInspected(context.Background(), []string{"asset.bin"}, expected)
			} else {
				err = repo.StageWithLFS(context.Background(), []string{"asset.bin"})
			}
			if err == nil {
				t.Fatal("clean 修改源文件后仍发布了索引")
			}
			token, err := runDigest(context.Background(), root, 30*time.Second, "ls-files", "--stage", "-z")
			if err != nil || token != expected.Token {
				t.Fatal("拒绝转换仍修改了真实索引")
			}
			data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
			if err != nil || string(data) != "*.txt text\n# user draft\n" {
				t.Fatalf("没有恢复用户属性草稿：%q %v", data, err)
			}
			data, err = os.ReadFile(filepath.Join(root, "asset.bin"))
			if err != nil || string(data) != "changed" {
				t.Fatal("覆盖了转换期间产生的工作区修改")
			}
			if staged := git(t, root, "show", ":.gitattributes"); staged != "*.txt text" {
				t.Fatal("没有保留原有已暂存属性：", staged)
			}
		})
	}
}

func TestLFSStageRejectsIndexFlagMutationDuringClean(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, ".gitattributes", "*.txt text\n")
	git(t, root, "add", ".gitattributes")
	write(t, root, ".gitattributes", "*.txt text\n# user draft\n")
	write(t, root, "asset.bin", "payload")
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	expected, err := repo.InspectFiles(context.Background(), []string{"asset.bin"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_TEST_LFS_FLAG_INDEX", filepath.Join(repo.Path, ".git", "index"))
	if err := repo.StageWithLFSInspected(context.Background(), []string{"asset.bin"}, expected); err == nil {
		t.Fatal("LFS 转换覆盖了外部修改的索引标记")
	}
	output, err := repo.git(context.Background(), "ls-files", "-v", "--", ".gitattributes")
	if err != nil || output != "h .gitattributes\n" {
		t.Fatalf("未保留外部设置的 assume-unchanged 标记：%q %v", output, err)
	}
	token, err := runDigest(context.Background(), root, 30*time.Second, "ls-files", "--stage", "-z")
	if err != nil || token != expected.Token {
		t.Fatal("拒绝转换仍发布了新的索引条目")
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil || string(data) != "*.txt text\n# user draft\n" {
		t.Fatalf("未回滚自身追加的属性规则：%q %v", data, err)
	}
}

func TestLFSStatusDecodesGitCQuotedUnicodeAndLiteralQuotes(t *testing.T) {
	fakeLFS(t)
	repo, _ := fixture(t)
	path := filepath.Join(t.TempDir(), "status.json")
	document := `{"files":[{"name":"\"\\347\\233\\256\\345\\275\\225/space [1]*.bin\"","downloaded":true},{"name":"\"\\\"quoted\\\".bin\"","downloaded":false},{"name":"\"leading.bin","downloaded":true}]}`
	if err := os.WriteFile(path, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_TEST_LFS_JSON", path)
	status, err := repo.LFSStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []LFSFile{
		{Path: "目录/space [1]*.bin", Downloaded: true},
		{Path: "\"quoted\".bin", Downloaded: false},
		{Path: "\"leading.bin", Downloaded: true},
	}
	if len(status.Files) != len(expected) {
		t.Fatalf("文件数错误：%+v", status.Files)
	}
	for index, file := range expected {
		if status.Files[index] != file {
			t.Fatalf("Git C-quote 解码破坏了文件名：%+v，预期 %+v", status.Files[index], file)
		}
	}
	if err := os.WriteFile(path, []byte(`{"files":[{"name":"\"\\056\\056/outside.bin\"","downloaded":false}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LFSStatus(context.Background()); err == nil {
		t.Fatal("C-quote 解码后的越界路径绕过了安全检查")
	}
}

func TestLFSSelectionPreservesUserHookGitShSetup(t *testing.T) {
	for _, initialize := range []bool{false, true} {
		t.Run(fmt.Sprint(initialize), func(t *testing.T) {
			fakeLFS(t)
			repo, root := fixture(t)
			path, err := ResolveExecutable(currentExecutableOptions())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			probe := exec.CommandContext(ctx, path, "--exec-path")
			probe.Env = gitEnvironment()
			output, err := probe.Output()
			if err != nil {
				t.Fatal(err)
			}
			expected := strings.TrimSpace(string(output))
			if _, err := os.Stat(filepath.Join(expected, "git-sh-setup")); err != nil {
				t.Fatal("所选 Git 缺少标准 hook 辅助程序：", err)
			}
			marker := filepath.Join(t.TempDir(), "hook-exec-path")
			t.Setenv("CODE_TEST_HELPER_LOG", marker)
			hook := filepath.Join(root, ".git", "hooks", "pre-commit")
			script := "#!/bin/sh\n. \"$(git --exec-path)/git-sh-setup\" || exit\nprintf '%s' \"$(git --exec-path)\" > \"$CODE_TEST_HELPER_LOG\"\n"
			if err := os.WriteFile(hook, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			if initialize {
				if err := repo.InitializeLFS(ctx); err != nil {
					t.Fatal(err)
				}
			}
			write(t, root, "ordinary.txt", "ordinary Git commit\n")
			if err := repo.Stage(ctx, []string{"ordinary.txt"}); err != nil {
				t.Fatal(err)
			}
			if err := repo.Commit(ctx, "preserve user hook"); err != nil {
				t.Fatal("可选 LFS 破坏了用户 hook 的 Git helper 路径：", err)
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != expected {
				t.Fatalf("hook 中的 Git exec-path 被替换：%q，预期 %q：%v", data, expected, err)
			}
		})
	}
}

func TestLFSInspectionRejectsDifferentToolInGitExecPath(t *testing.T) {
	lfsPath := fakeLFS(t)
	repo, _ := fixture(t)
	realGit, err := ResolveExecutable(currentExecutableOptions())
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	gitPath := filepath.Join(directory, "git")
	execDirectory := filepath.Join(directory, "git-core")
	if err := os.MkdirAll(execDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_TEST_REAL_GIT", realGit)
	t.Setenv("CODE_TEST_GIT_EXEC_PATH", execDirectory)
	script := "#!/bin/sh\nif [ \"$1\" = --exec-path ]; then printf '%s\\n' \"$CODE_TEST_GIT_EXEC_PATH\"; exit; fi\nexec \"$CODE_TEST_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(gitPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	previous := currentExecutableOptions()
	t.Cleanup(func() { ConfigureExecutable(previous) })
	ConfigureExecutable(ExecutableOptions{Source: "custom", Path: gitPath})
	shadow := filepath.Join(execDirectory, "git-lfs")
	if err := os.Symlink(lfsPath, shadow); err != nil {
		t.Fatal(err)
	}
	options := ExecutableOptions{Source: "custom", Path: lfsPath}
	if _, err := InspectLFSExecutable(context.Background(), options); err != nil {
		t.Fatal("Git exec-path 引用同一 LFS 工具时不应拒绝：", err)
	}
	if err := os.Remove(shadow); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectLFSExecutable(context.Background(), options); err == nil || !strings.Contains(err.Error(), "exec-path") {
		t.Fatalf("未拒绝会覆盖所选 LFS 的 Git exec-path：%v", err)
	}
	if _, err := repo.Snapshot(context.Background()); err != nil {
		t.Fatal("不支持的可选 LFS 组合阻断了普通 Git：", err)
	}
}

func configureLFSInterceptGit(t *testing.T, script string) {
	t.Helper()
	realGit, err := ResolveExecutable(currentExecutableOptions())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "git")
	t.Setenv("CODE_TEST_REAL_GIT", realGit)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\nexec \"$CODE_TEST_REAL_GIT\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	previous := executableOptions.Load()
	t.Cleanup(func() { executableOptions.Store(previous) })
	ConfigureExecutable(ExecutableOptions{Source: "custom", Path: path})
}

func TestLFSStagePublicationProtectsConcurrentIndexEdit(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, ".gitattributes", "# base\n")
	git(t, root, "add", ".gitattributes")
	write(t, root, "asset.bin", "payload")
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	hash, err := run(context.Background(), root, "# base\n# external staged only\n", 30*time.Second, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "flags")
	result := filepath.Join(t.TempDir(), "external-result")
	t.Setenv("CODE_TEST_FLAG_MARKER", marker)
	t.Setenv("CODE_TEST_EXTERNAL_RESULT", result)
	t.Setenv("CODE_TEST_EXTERNAL_OID", strings.TrimSpace(hash))
	t.Setenv("CODE_TEST_ACTUAL_INDEX", filepath.Join(root, ".git", "index"))
	configureLFSInterceptGit(t, `
case "$*" in
*"ls-files -v -z --"*)
  "$CODE_TEST_REAL_GIT" "$@" || exit
  if [ -f "$CODE_TEST_FLAG_MARKER" ]; then
    GIT_INDEX_FILE="$CODE_TEST_ACTUAL_INDEX" "$CODE_TEST_REAL_GIT" update-index --cacheinfo "100644,$CODE_TEST_EXTERNAL_OID,.gitattributes" >/dev/null 2>&1
    printf '%s' "$?" > "$CODE_TEST_EXTERNAL_RESULT"
  else
    touch "$CODE_TEST_FLAG_MARKER"
  fi
  exit 0 ;;
esac`)
	stageErr := repo.StageWithLFS(context.Background(), []string{"asset.bin"})
	outcome, err := os.ReadFile(result)
	if err != nil {
		t.Fatal("没有触发最终索引检查：", err)
	}
	if string(outcome) == "0" && !strings.Contains(git(t, root, "show", ":.gitattributes"), "# external staged only") {
		t.Fatalf("覆盖了检查后发生的外部暂存：%v", stageErr)
	}
}

func TestLFSStageCancellationCleansOwnedIndexLock(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	write(t, root, "asset.bin", "payload")
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "update")
	blocked := filepath.Join(t.TempDir(), "blocked")
	t.Setenv("CODE_TEST_UPDATE_MARKER", marker)
	t.Setenv("CODE_TEST_UPDATE_BLOCKED", blocked)
	t.Setenv("CODE_TEST_ACTUAL_INDEX", filepath.Join(root, ".git", "index"))
	configureLFSInterceptGit(t, `
case "$*" in
*"update-index --add -z --index-info"*)
  if [ -f "$CODE_TEST_UPDATE_MARKER" ]; then
    lock="${GIT_INDEX_FILE:-$CODE_TEST_ACTUAL_INDEX}.lock"
    (set -C; : > "$lock") || exit
    touch "$CODE_TEST_UPDATE_BLOCKED"
    sleep 60
    exit 99
  fi
  touch "$CODE_TEST_UPDATE_MARKER" ;;
esac`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- repo.StageWithLFS(ctx, []string{"asset.bin"}) }()
	for {
		if _, err := os.Stat(blocked); err == nil {
			cancel()
			break
		}
		select {
		case err := <-done:
			t.Fatalf("未进入发布阶段便退出：%v", err)
		case <-ctx.Done():
			t.Fatal("未进入索引发布阶段")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := <-done; err == nil {
		t.Fatal("取消发布未报错")
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "index.lock")); !os.IsNotExist(err) {
		t.Fatalf("取消遗留了真实索引锁：%v", err)
	}
	if got := git(t, root, "ls-files"); got != "" {
		t.Fatalf("取消后仍发布了索引：%q", got)
	}
}

func TestLFSAttributeRollbackPreservesOpenEditorFile(t *testing.T) {
	fakeLFS(t)
	repo, root := fixture(t)
	before := "# user draft\n"
	write(t, root, ".gitattributes", before)
	write(t, root, "asset.bin", "payload")
	if err := repo.InitializeLFS(context.Background()); err != nil {
		t.Fatal(err)
	}
	editor, err := os.OpenFile(filepath.Join(root, ".gitattributes"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer editor.Close()
	t.Setenv("CODE_TEST_LFS_FAIL", "1")
	if err := repo.StageWithLFS(context.Background(), []string{"asset.bin"}); err == nil {
		t.Fatal("忽略了过滤器错误")
	}
	if _, err := editor.WriteString("# late editor write\n"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil || string(data) != before {
		t.Fatalf("回滚仍原地截断用户编辑器持有的文件：%q %v", data, err)
	}
	backups, err := filepath.Glob(filepath.Join(root, ".git", ".51code-lfs-attributes-*", ".gitattributes"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("缺少保留并发编辑的原文件：%v %v", backups, err)
	}
	data, err = os.ReadFile(backups[0])
	if err != nil || !strings.Contains(string(data), "# late editor write\n") || !strings.Contains(string(data), "filter=lfs") {
		t.Fatalf("原文件中的规则或并发编辑丢失：%q %v", data, err)
	}
}

func TestLFSAttributeRollbackPreservesReplacedNewFile(t *testing.T) {
	repo, directory := fixture(t)
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	after, err := appendLFSAttributes(root, lfsAttributes{}, "asset.bin filter=lfs\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Rename(".gitattributes", "original-attributes"); err != nil {
		t.Fatal(err)
	}
	write(t, directory, ".gitattributes", after.content)
	info, err := root.Lstat(".gitattributes")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.rollbackLFSAttributes(root, lfsAttributes{}, after); err == nil {
		t.Fatal("回滚接受了编辑器替换的新文件")
	}
	current, err := root.Lstat(".gitattributes")
	if err != nil || !os.SameFile(info, current) {
		t.Fatalf("回滚删除或替换了编辑器保存的文件：%v", err)
	}
}
