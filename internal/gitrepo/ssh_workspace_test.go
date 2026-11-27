// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSSHWorkspaceWriteOperations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("本地 SSH 替身需要 POSIX shell")
	}
	_, dir := fixture(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexec sh -s\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx := t.Context()
	repo := SSHWorkspace{Host: "fixture", Path: dir}
	snapshot := func() Snapshot {
		t.Helper()
		result, _, err := repo.Snapshot(ctx)
		if err != nil || !result.SSHWritable {
			t.Fatalf("远端工作树不可写: %+v %v", result, err)
		}
		return result
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	file := "-it's [a] file"
	write(t, dir, file, "first\n")
	initial := snapshot()
	large, err := repo.LargeFiles(ctx, []string{file}, "", 2)
	must(err)
	if len(large) != 1 || large[0].Path != file || large[0].Size != 6 {
		t.Fatalf("暂存前大文件检查错误: %+v", large)
	}
	must(repo.StageFiles(ctx, initial, []string{file}, false))
	must(repo.StageFiles(ctx, initial, []string{file}, true))
	if len(git(t, dir, "ls-files")) != 0 {
		t.Fatal("未出生分支取消暂存失败")
	}
	must(repo.StageFiles(ctx, initial, []string{file}, false))
	inspection, err := repo.PrepareCommit(ctx, initial, true)
	must(err)
	large, err = repo.LargeFiles(ctx, nil, inspection.Token, 2)
	must(err)
	if len(large) != 1 || large[0].Path != file || large[0].Size != 6 {
		t.Fatalf("提交前大文件检查错误: %+v", large)
	}
	write(t, dir, file, "second\n")
	must(repo.StageFiles(ctx, initial, []string{file}, false))
	if err := repo.CommitStaged(ctx, initial, "stale", inspection.Token); err == nil {
		t.Fatal("暂存内容变化后仍允许提交")
	}
	inspection, err = repo.PrepareCommit(ctx, initial, true)
	must(err)
	message := "first 'commit'\n\nwith a body"
	must(repo.CommitStaged(ctx, initial, message, inspection.Token))
	if git(t, dir, "log", "-1", "--format=%B") != message {
		t.Fatal("提交说明未保留")
	}
	main := snapshot()
	git(t, dir, "branch", "topic")
	must(repo.SwitchLocalBranch(ctx, main, "topic"))
	if err := repo.SwitchLocalBranch(ctx, main, "main"); err == nil {
		t.Fatal("分支改变后仍接受旧窗口的操作")
	}
	must(repo.SwitchLocalBranch(ctx, snapshot(), "main"))
	bare := t.TempDir()
	git(t, bare, "init", "--bare", "-b", "main")
	git(t, dir, "remote", "add", "origin", bare)
	must(repo.SyncBranch(ctx, snapshot(), true))
	if git(t, bare, "rev-parse", "main") != main.Head || snapshot().Upstream != "origin/main" {
		t.Fatal("首次推送未发布当前分支或建立跟踪")
	}
	git(t, dir, "update-ref", "-d", "refs/remotes/origin/main")
	missingTracking := snapshot()
	if missingTracking.Upstream != "origin/main" || missingTracking.SSHRemoteRef != "refs/heads/main" {
		t.Fatalf("丢失跟踪引用后应保留已配置的上游: %+v", missingTracking)
	}
	must(repo.SyncBranch(ctx, missingTracking, true))
	writer := t.TempDir()
	git(t, writer, "clone", bare, ".")
	git(t, writer, "config", "user.name", "Writer")
	git(t, writer, "config", "user.email", "user@example.test")
	write(t, writer, "extra", "remote")
	git(t, writer, "add", "extra")
	git(t, writer, "commit", "-m", "extra")
	git(t, writer, "push")
	write(t, dir, ".git/info/exclude", "extra\n")
	write(t, dir, "extra", "private")
	if err := repo.SyncBranch(ctx, snapshot(), false); err == nil {
		t.Fatal("拉取可能覆盖被忽略文件时应拒绝")
	}
	kept, err := os.ReadFile(filepath.Join(dir, "extra"))
	must(err)
	if string(kept) != "private" {
		t.Fatal("拉取覆盖了忽略文件")
	}
	must(os.Remove(filepath.Join(dir, "extra")))
	git(t, dir, "switch", "-c", "feat/a=1")
	git(t, dir, "branch", "--set-upstream-to=origin/main")
	git(t, dir, "config", "branch.feat/a=1.mergeOptions", "--invalid-merge-option")
	must(repo.SyncBranch(ctx, snapshot(), false))
	if got := snapshot(); got.Branch != "feat/a=1" || got.Head == main.Head || got.Head != git(t, writer, "rev-parse", "HEAD") {
		t.Fatalf("带等号的远端分支未快进: %+v", got)
	}
	git(t, writer, "commit", "--allow-empty", "-m", "upstream")
	git(t, writer, "push")
	git(t, dir, "commit", "--allow-empty", "-m", "local")
	diverged := snapshot()
	if repo.SyncBranch(ctx, diverged, false) == nil || repo.SyncBranch(ctx, diverged, true) == nil || snapshot().Head != diverged.Head {
		t.Fatal("分叉不应合并或强制推送")
	}
}

func TestSSHWorkspaceBatchesLargeFileLists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("本地 SSH 替身需要 POSIX shell")
	}
	_, dir := fixture(t)
	write(t, dir, "base", "base\n")
	git(t, dir, "add", "base")
	git(t, dir, "commit", "-m", "base")
	head := git(t, dir, "rev-parse", "HEAD")
	var paths []string
	for index := range 200 {
		name := strconv.Itoa(index) + "-" + strings.Repeat("long-path-'", 17)
		write(t, dir, name, "large\n")
		paths = append(paths, name)
	}
	write(t, dir, "small", "x")
	git(t, dir, "add", "small")
	git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",vendor/module")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	write(t, bin, "ssh", "#!/bin/sh\nexec sh -s\n")
	write(t, bin, "git", `#!/bin/sh
case " $* " in
    *' ls-tree '*)
        case " $* " in *' ls-tree -l -z '*) ;; *) exit 98;; esac
        printf 'ls-tree\n' >> "$SSH_TEST_BATCH_LOG"
        ;;
    *' add '*) printf 'add\n' >> "$SSH_TEST_BATCH_LOG";;
    *' restore --staged '*) printf 'restore\n' >> "$SSH_TEST_BATCH_LOG";;
esac
exec "$SSH_TEST_GIT" "$@"
`)
	for _, name := range []string{"ssh", "git"} {
		if err := os.Chmod(filepath.Join(bin, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(bin, "batches.log")
	t.Setenv("SSH_TEST_GIT", gitPath)
	t.Setenv("SSH_TEST_BATCH_LOG", log)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx := t.Context()
	repo := SSHWorkspace{Host: "fixture", Path: dir}
	snapshot, _, err := repo.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.StageFiles(ctx, snapshot, paths, false); err != nil {
		t.Fatal(err)
	}
	prepared, err := repo.PrepareCommit(ctx, snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	large, err := repo.LargeFiles(ctx, nil, prepared.Token, 2)
	if err != nil || len(large) != len(paths) {
		t.Fatalf("应跨批次返回大 blob 并跳过小文件和 gitlink，条目数 %d: %v", len(large), err)
	}
	slices.Sort(paths)
	slices.SortFunc(large, func(a, b FileInspection) int { return strings.Compare(a.Path, b.Path) })
	for index, file := range large {
		if file.Path != paths[index] || file.Size != 6 {
			t.Fatalf("文件大小或路径错误: %+v，预期 %s", file, paths[index])
		}
	}
	if err := repo.StageFiles(ctx, snapshot, paths, true); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "diff", "--cached", "--name-only"); got != "small\nvendor/module" {
		t.Fatalf("取消暂存后索引内容错误: %q", got)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(commands), "add\n") != 1 || strings.Count(string(commands), "restore\n") != 1 ||
		strings.Count(string(commands), "ls-tree\n") < 2 {
		t.Fatalf("应一次更新索引并分批检查大小: %s", commands)
	}
	write(t, dir, ".gitattributes", strconv.Quote(paths[len(paths)-1])+" filter=unavailable\n")
	git(t, dir, "config", "filter.unavailable.clean", "false")
	git(t, dir, "config", "filter.unavailable.required", "true")
	if err := repo.StageFiles(ctx, snapshot, paths, false); err == nil {
		t.Fatal("过滤器失败时应拒绝暂存")
	}
	if got := git(t, dir, "diff", "--cached", "--name-only"); got != "small\nvendor/module" {
		t.Fatalf("批量暂存失败后索引应保持原样: %q", got)
	}
}

func TestSSHUnbornSnapshotKeepsNestedRefsOutOfUpstream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("本地 SSH 替身需要 POSIX shell")
	}
	_, dir := fixture(t)
	git(t, dir, "commit", "--allow-empty", "-m", "base")
	git(t, dir, "branch", "--track", "topic/sub", "main")
	git(t, dir, "symbolic-ref", "HEAD", "refs/heads/topic")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexec sh -s\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := SSHWorkspace{Host: "fixture", Path: dir}
	snapshot, refs, err := repo.Snapshot(t.Context())
	if err != nil || !snapshot.Unborn || snapshot.Branch != "topic" || snapshot.Upstream != "" ||
		snapshot.SSHRemote != "" || snapshot.SSHRemoteRef != "" || !slices.Equal(refs, []string{"refs/heads/main", "refs/heads/topic/sub"}) {
		t.Fatalf("未出生分支混入了子命名空间的上游: %+v，引用 %v: %v", snapshot, refs, err)
	}
}

func TestSSHCommitKeepsReviewedIndexWhileAnotherProcessStages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("本地 SSH 替身需要 POSIX shell")
	}
	_, dir := fixture(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexec sh -s\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	write(t, dir, "reviewed.txt", "reviewed\n")
	write(t, dir, "concurrent.txt", "concurrent\n")
	git(t, dir, "add", "reviewed.txt")
	hook := `#!/bin/sh
if (unset GIT_INDEX_FILE; git add -- concurrent.txt 2>/dev/null); then
    printf 'accepted' > .git/parallel-stage
else
    printf 'blocked' > .git/parallel-stage
fi
`
	if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-commit"), []byte(hook), 0700); err != nil {
		t.Fatal(err)
	}
	repo := SSHWorkspace{Host: "fixture", Path: dir}
	snapshot, _, err := repo.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	review, err := repo.PrepareCommit(t.Context(), snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitStaged(t.Context(), snapshot, "reviewed commit", review.Token); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(filepath.Join(dir, ".git", "parallel-stage"))
	if err != nil || string(result) != "blocked" || git(t, dir, "rev-parse", "HEAD^{tree}") != review.Token {
		t.Fatalf("提交期间应保护已检查的暂存树: %q, %v", result, err)
	}
	git(t, dir, "add", "concurrent.txt")
	if got := git(t, dir, "diff", "--cached", "--name-only"); got != "concurrent.txt" {
		t.Fatalf("提交后应释放索引锁: %s", got)
	}
}

func TestSSHCommitRejectsHookTreeChangesAndPreservesHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("本地 SSH 替身需要 POSIX shell")
	}
	_, dir := fixture(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexec sh -s\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git(t, dir, "commit", "--allow-empty", "-m", "base")
	git(t, dir, "config", "core.hooksPath", ".hooks")
	if err := os.Mkdir(filepath.Join(dir, ".hooks"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"pre-commit":            "printf 'normalized\\n' > note.txt && git add -- note.txt",
		"commit-msg":            "printf '\\nchecked message\\n' >> \"$1\"",
		"reference-transaction": "cat >> .git/hook-refs\nprintf '%s\\n' \"$1\" >> .git/hook-events",
		"post-commit":           "touch .git/hook-post",
	} {
		if err := os.WriteFile(filepath.Join(dir, ".hooks", name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write(t, dir, "note.txt", "normalized   \n")
	git(t, dir, "add", "note.txt")
	repo := SSHWorkspace{Host: "fixture", Path: dir}
	ctx := t.Context()
	expected, _, err := repo.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	review, err := repo.PrepareCommit(ctx, expected, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitStaged(ctx, expected, "format note", review.Token); err == nil || !strings.Contains(err.Error(), "提交钩子修改") {
		t.Fatalf("应在写入引用前拒绝格式化后的不同提交树: %v", err)
	}
	if git(t, dir, "rev-parse", "HEAD") != expected.Head || git(t, dir, "write-tree") != review.Token {
		t.Fatal("拒绝提交后应保留分支和原索引")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "hook-post")); !os.IsNotExist(err) {
		t.Fatalf("拒绝提交后不应调用 post-commit: %v", err)
	}
	git(t, dir, "add", "note.txt")
	review, err = repo.PrepareCommit(ctx, expected, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitStaged(ctx, expected, "format note", review.Token); err != nil {
		t.Fatal(err)
	}
	if git(t, dir, "rev-parse", "HEAD^{tree}") != review.Token ||
		!strings.Contains(git(t, dir, "log", "-1", "--format=%B"), "checked message") {
		t.Fatal("已核对的提交应使用原钩子修改过的提交说明")
	}
	events, err := os.ReadFile(filepath.Join(dir, ".git", "hook-events"))
	if err != nil || !strings.Contains(string(events), "prepared\ncommitted\n") {
		t.Fatalf("原引用事务钩子应收到准备和提交通知: %q, %v", events, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "hook-post")); err != nil {
		t.Fatal(err)
	}
}

func TestSSHWorkspaceBrowseAndInsightsMatchLocal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("本地替身需要 POSIX shell")
	}
	local, dir := fixture(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexec sh -s\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	remote := SSHWorkspace{Host: "fixture", Path: dir}
	ctx := t.Context()
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	until := from.AddDate(0, 0, 3)
	if empty, err := remote.Insights(ctx, "HEAD", from, until); err != nil || empty.Commits != 0 {
		t.Fatalf("空仓库洞察错误: %+v %v", empty, err)
	}
	file := "sub/a ' $file\nname.txt"
	write(t, dir, file, "initial\n")
	write(t, dir, "unchanged.txt", "unchanged\n")
	write(t, dir, ".gitignore", "ignored.txt\n")
	git(t, dir, "add", ".")
	first := insightsCommit(t, dir, "2026-11-01T12:00:00Z", "Alias", "alias@example.test")
	git(t, dir, "branch", "first")
	for range 100 {
		insightsCommit(t, dir, "2026-11-02T12:00:00Z", "Canonical", "canonical@example.test")
	}
	filter := HistoryFilter{Mode: "author"}
	firstPage, err := remote.HistoryPage(ctx, "", "canonical", "", filter, 0, 64)
	if err != nil || len(firstPage) != 64 {
		t.Fatalf("first search page: %d results, %v", len(firstPage), err)
	}
	secondPage, err := remote.HistoryPage(ctx, "", "canonical", "", filter, len(firstPage), 64)
	if err != nil || len(secondPage) != 36 || slices.ContainsFunc(firstPage, func(commit Commit) bool {
		return commit.Hash == secondPage[0].Hash
	}) {
		t.Fatalf("second search page: %d results, %v", len(secondPage), err)
	}
	write(t, dir, ".mailmap", "Canonical <canonical@example.test> Alias <alias@example.test>\n")
	write(t, dir, file, "staged\n")
	git(t, dir, "add", "--", file)
	write(t, dir, file, "worktree\n")
	write(t, dir, "new.txt", "new\n")
	write(t, dir, "ignored.txt", "ignored\n")
	for _, revision := range []string{"WORKTREE", "INDEX", "HEAD", first} {
		wantFiles, err := local.BrowseFiles(ctx, revision)
		if err != nil {
			t.Fatal(err)
		}
		gotFiles, err := remote.BrowseFiles(ctx, revision)
		if err != nil || !slices.Equal(gotFiles, wantFiles) {
			t.Fatalf("%s files = %q, want %q: %v", revision, gotFiles, wantFiles, err)
		}
		want, err := local.BrowseFileContent(ctx, file, revision)
		if err != nil {
			t.Fatal(err)
		}
		got, err := remote.BrowseFileContent(ctx, file, revision)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s content = %+v, want %+v: %v", revision, got, want, err)
		}
	}
	if content, err := remote.BrowseFileContent(ctx, "unchanged.txt", "WORKTREE"); err != nil || content.Text != "unchanged\n" {
		t.Fatalf("未改动文件: %+v %v", content, err)
	} else if blame, err := remote.BlameLine(ctx, content, 1); err != nil || blame.Hash != first {
		t.Fatalf("远程行归属: %+v %v", blame, err)
	}
	for _, revision := range []string{"WORKTREE", first} {
		if history, err := remote.FileHistory(ctx, revision, file); err != nil || len(history) != 1 || history[0].Hash != first {
			t.Fatalf("远程文件历史 %s: %+v %v", revision, history, err)
		}
	}
	for _, name := range []string{"ignored.txt", "../outside", ".git/config"} {
		if _, err := remote.BrowseFileContent(ctx, name, "WORKTREE"); err == nil {
			t.Errorf("读取了范围外的路径 %q", name)
		}
	}
	write(t, dir, "big.txt", strings.Repeat("x", fileContentLimit+1))
	if content, err := remote.BrowseFileContent(ctx, "big.txt", "WORKTREE"); err != nil || !content.Truncated {
		t.Fatalf("大型远程文件: %+v %v", content, err)
	}
	outside := t.TempDir()
	write(t, outside, filepath.Base(file), "outside content\n")
	linkPath := filepath.Join(dir, "link.txt")
	if err := os.Symlink(filepath.Join(outside, filepath.Base(file)), linkPath); err != nil {
		t.Fatal(err)
	}
	if content, err := remote.BrowseFileContent(ctx, "link.txt", "WORKTREE"); err != nil || !content.Unsupported || content.Text != "" {
		t.Fatalf("远程链接: %+v %v", content, err)
	}
	if err := os.Rename(filepath.Join(dir, "sub"), filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	if content, err := remote.BrowseFileContent(ctx, file, "WORKTREE"); err != nil || content.Present || content.Unsupported {
		t.Fatalf("父目录被删除时应仅标记文件不存在: %+v %v", content, err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	if content, err := remote.BrowseFileContent(ctx, file, "WORKTREE"); err != nil || !content.Unsupported || content.Text != "" {
		t.Fatalf("远程链接祖先: %+v %v", content, err)
	}
	for ref, count := range map[string]int{"HEAD": 101, "refs/heads/first": 1} {
		source, err := local.InsightsSource(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		want, err := local.Insights(ctx, source, from, until)
		if err != nil {
			t.Fatal(err)
		}
		got, err := remote.Insights(ctx, ref, from, until)
		if err != nil || got.Commits != count || !reflect.DeepEqual(got, want) {
			t.Fatalf("洞察 %s = %+v，期望 %+v: %v", ref, got, want, err)
		}
	}
}

func TestReadSSHDirectoryPreservesRemotePathNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("本地替身需要 POSIX shell")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".git", ".hidden", "quote ' $HOME", "file\nname"} {
		if err := os.Mkdir(filepath.Join(base, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "ordinary-file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexec sh -s\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", base)
	dir, err := ReadSSHDirectory(t.Context(), "devbox", "")
	want := []string{filepath.Join(base, ".hidden"), filepath.Join(base, "quote ' $HOME")}
	if err != nil || dir.Path != base || !slices.Equal(dir.Children, want) {
		t.Fatalf("directory = %+v, want %q with %q: %v", dir, base, want, err)
	}
	child, err := ReadSSHDirectory(t.Context(), "devbox", want[1])
	if err != nil || child.Path != want[1] || len(child.Children) != 0 {
		t.Fatalf("child = %+v, want empty directory %q: %v", child, want[1], err)
	}
	if _, err := ReadSSHDirectory(t.Context(), "devbox", filepath.Join(base, "missing")); err == nil {
		t.Fatal("不存在的远端目录应返回错误")
	}
}

func TestSSHWorkspaceRejectsInvalidDestination(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, target := range [][2]string{
		{"", "/repo"}, {"-V", "/repo"}, {"host name", "/repo"}, {"user@host;extra", "/repo"},
		{"host", "relative"}, {"host", "/repo\nother"}, {"host", "/repo\x00"},
	} {
		if _, _, err := (SSHWorkspace{target[0], target[1]}).Snapshot(t.Context()); err == nil || errors.Is(err, exec.ErrNotFound) {
			t.Errorf("accepted %q", target)
		}
	}
}

func TestSSHWorkspaceRejectsInvalidGitArgumentsBeforeConnecting(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	ctx := t.Context()
	repo := SSHWorkspace{Host: "fixture", Path: "/repo"}
	for _, ref := range []string{"refs/heads/x\x00", "refs/heads/x\r", "refs/heads/x\n"} {
		if _, err := repo.HistoryPage(ctx, ref, "", "", HistoryFilter{}, 0, 1); err == nil || errors.Is(err, exec.ErrNotFound) {
			t.Errorf("未在连接前拒绝历史引用 %q: %v", ref, err)
		}
		if _, err := repo.Insights(ctx, ref, time.Time{}, time.Time{}); err == nil || errors.Is(err, exec.ErrNotFound) {
			t.Errorf("未在连接前拒绝洞察引用 %q: %v", ref, err)
		}
	}
	if _, err := repo.PrepareCommit(ctx, Snapshot{Head: "invalid-head"}, false); err == nil || errors.Is(err, exec.ErrNotFound) {
		t.Errorf("未在连接前拒绝无效 HEAD: %v", err)
	}
	if _, err := runSSHScript(ctx, repo.Host, "printf '\x00'"); err == nil || errors.Is(err, exec.ErrNotFound) {
		t.Errorf("未在连接前拒绝含 NUL 的脚本: %v", err)
	}
}

func TestSSHWorkspaceReadsRemoteGitViews(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("本地替身需要 POSIX shell")
	}
	_, root := fixture(t)
	path := filepath.Join(t.TempDir(), "work tree ' $HOME")
	if err := os.Rename(root, path); err != nil {
		t.Fatal(err)
	}
	write(t, path, "demo.txt", "base\n")
	git(t, path, "add", "demo.txt")
	git(t, path, "commit", "-m", "remote fixture")
	write(t, path, "demo.txt", "staged\n")
	git(t, path, "add", "demo.txt")
	write(t, path, "demo.txt", "working\n")
	bin := t.TempDir()
	shim := `#!/bin/sh
while [ "$1" != "--" ] && [ "$#" -gt 0 ]; do shift; done
[ "$#" = 3 ] && [ "$2" = devbox ] || exit 2
exec sh -c "$3"
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := SSHWorkspace{"devbox", path}
	ctx := t.Context()
	snapshot, refs, err := repo.Snapshot(ctx)
	if err != nil || snapshot.Branch != "main" || len(refs) != 1 || len(snapshot.Files) != 1 {
		t.Fatalf("snapshot %+v, refs %v: %v", snapshot, refs, err)
	}
	history, err := repo.History(ctx, refs[0])
	if err != nil || len(history) != 1 || history[0].Subject != "remote fixture" {
		t.Fatalf("history %+v: %v", history, err)
	}
	files, err := repo.CommitFiles(ctx, history[0].Hash)
	if err != nil || len(files) != 1 || files[0].Path != "demo.txt" {
		t.Fatalf("files %+v: %v", files, err)
	}
	for _, check := range []struct {
		staged bool
		commit string
		want   string
		old    string
		next   string
	}{
		{false, "", "-staged\n+working", "staged\n", "working\n"},
		{true, "", "-base\n+staged", "base\n", "staged\n"},
		{false, history[0].Hash, "+base", "", "base\n"},
	} {
		diff, err := repo.FileDiff(ctx, snapshot.Files[0], check.staged, check.commit)
		if err != nil || !strings.Contains(diff.Text, check.want) || diff.Old == nil || diff.New == nil ||
			diff.Old.Text != check.old || diff.New.Text != check.next {
			t.Errorf("wanted %q and both previews, got %+v, old %+v, new %+v: %v", check.want, diff, diff.Old, diff.New, err)
		}
	}
	name := "it's a $file\nwith newline"
	write(t, path, name, "a new file\n")
	diff, err := repo.FileDiff(ctx, File{Path: name, Working: "?"}, false, "")
	if err != nil || !strings.Contains(diff.Text, "+a new file") || diff.Old == nil || diff.Old.Present ||
		diff.New == nil || diff.New.Text != "a new file\n" {
		t.Fatalf("untracked file: %q, %v", diff.Text, err)
	}
	paths, err := repo.WorktreeSearchPaths(ctx)
	if err != nil || !slices.Equal(paths, []string{"demo.txt", name}) {
		t.Fatalf("quick open: %q, %v", paths, err)
	}
	var matches []WorktreeSearchMatch
	publish := func(batch []WorktreeSearchMatch) { matches = append(matches, batch...) }
	write(t, path, "legacy-encoding.txt", "new file\xff\n")
	git(t, path, "config", "submodule.recurse", "true")
	git(t, path, "config", "grep.column", "true")
	if limited, err := repo.SearchWorktree(ctx, "NEW FILE", publish); limited || err != nil ||
		!slices.Equal(matches, []WorktreeSearchMatch{{Path: name, Line: 1, Column: 2, Text: "a new file"}}) {
		t.Fatalf("worktree grep: %+v, limited %v: %v", matches, limited, err)
	}
	matches = nil
	if limited, err := repo.SearchWorktree(ctx, "absent", publish); limited || err != nil || len(matches) != 0 {
		t.Fatalf("worktree grep without matches: %+v, limited %v: %v", matches, limited, err)
	}
	git(t, path, "config", "--unset", "submodule.recurse")
	git(t, path, "add", "--", name)
	if _, err := repo.FileDiff(ctx, File{Path: name, Working: "?"}, false, ""); err == nil {
		t.Fatal("已暂存的文件仍使用过期的未跟踪状态展示")
	}
	if err := os.Rename(filepath.Join(path, "demo.txt"), filepath.Join(path, "moved.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, path, "demo.txt/child.txt", "nested content\n")
	git(t, path, "add", "-A")
	git(t, path, "commit", "-m", "replace file with directory")
	target := git(t, path, "rev-parse", "HEAD")
	diff, err = repo.ComparisonDiff(ctx, history[0].Hash, target, CommitFile{Path: "demo.txt", Status: "D"})
	if err != nil || !strings.Contains(diff.Text, "-base") || strings.Contains(diff.Text, "nested content") {
		t.Fatalf("文件被同名目录替换，选中文件的补丁错误: %q, %v", diff.Text, err)
	}
	diff, err = repo.FileDiff(ctx, File{Path: "demo.txt"}, false, target)
	if err != nil || !strings.Contains(diff.Text, "-base") || strings.Contains(diff.Text, "nested content") {
		t.Fatalf("提交文件差异混入了同名目录的补丁: %q, %v", diff.Text, err)
	}
	repo.Path = filepath.Join(path, "missing")
	if _, _, err := repo.Snapshot(ctx); err == nil {
		t.Error("missing directory reported as an empty repository")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := repo.Snapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request returned %v", err)
	}
	if _, err := (SSHWorkspace{"devbox", path}).SearchWorktree(ctx, "working", publish); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled search returned %v", err)
	}
}

func TestSSHQuickOpenStreamsLargeFileList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("本地替身需要 POSIX shell")
	}
	bin := t.TempDir()
	fixture := filepath.Join(bin, "paths")
	record := strings.Repeat("segment/", 24) + "file.txt\x00"
	raw := strings.Repeat(record, outputLimit/len(record)+1) + "last.txt\x00"
	if err := os.WriteFile(fixture, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\ncat >/dev/null\nexec cat \"$SSH_PATHS_FIXTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SSH_PATHS_FIXTURE", fixture)
	paths, err := (SSHWorkspace{Host: "fixture", Path: "/repo"}).WorktreeSearchPaths(t.Context())
	if err != nil || !slices.Equal(paths, []string{"last.txt", strings.TrimSuffix(record, "\x00")}) {
		t.Fatalf("路径应完整传输、去重并排序: %q, %v", paths, err)
	}
}

func TestSSHWorkspaceRepositoryInformation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("本地替身需要 POSIX shell")
	}
	_, root := fixture(t)
	_, child := fixture(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexec sh -s\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	write(t, child, "child.txt", "submodule\n")
	git(t, child, "add", "child.txt")
	git(t, child, "commit", "-m", "submodule base")
	git(t, root, "-c", "protocol.file.allow=always", "submodule", "add", child, "vendor/child's copy")
	git(t, root, "commit", "-m", "root")
	base := git(t, root, "rev-parse", "HEAD")
	git(t, root, "commit", "--allow-empty", "-m", "lost")
	lost := git(t, root, "rev-parse", "HEAD")
	git(t, root, "reset", "--keep", base)
	write(t, root, "stashed.txt", "stash fixture\n")
	git(t, root, "stash", "push", "--include-untracked", "-m", "sample")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "-b", "linked", linked)
	linked, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "vendor/child's copy/child.txt", "modified submodule\n")
	repo := SSHWorkspace{Host: "fixture", Path: root}
	trees, err := (SSHWorkspace{Host: repo.Host, Path: linked}).Worktrees(t.Context())
	if err != nil || len(trees) != 2 || !trees[0].Main || !trees[1].Current || trees[1].Path != linked {
		t.Fatalf("worktrees: %+v, %v", trees, err)
	}
	modules, err := repo.Submodules(t.Context())
	if err != nil || len(modules) != 1 || modules[0].Path != "vendor/child's copy" || !modules[0].Dirty ||
		modules[0].Head == "" || modules[0].Head != modules[0].Target {
		t.Fatalf("submodules: %+v, %v", modules, err)
	}
	if content, err := repo.BrowseFileContent(t.Context(), "vendor/child's copy", "WORKTREE"); err != nil || !content.Present || !content.Unsupported {
		t.Fatalf("子模块工作区目录应是存在但不支持预览的条目: %+v, %v", content, err)
	}
	git(t, filepath.Join(root, "vendor/child's copy"), "symbolic-ref", "HEAD", "refs/heads/unborn")
	unborn, err := repo.Submodules(t.Context())
	if err != nil || len(unborn) != 1 || unborn[0].Path != modules[0].Path || unborn[0].Target != modules[0].Target ||
		unborn[0].Head != "" || !unborn[0].Dirty {
		t.Fatalf("应保留未出生子模块及其脏状态: %+v, %v", unborn, err)
	}
	stashes, err := repo.Stashes(t.Context())
	if err != nil || len(stashes) != 1 {
		t.Fatalf("stashes: %+v, %v", stashes, err)
	}
	if diff, err := repo.StashDiff(t.Context(), stashes[0].Hash); err != nil || !strings.Contains(diff.Text, "+stash fixture") {
		t.Fatalf("stash diff: %q, %v", diff.Text, err)
	}
	reflog, err := repo.LostCommits(t.Context())
	if err != nil || !slices.ContainsFunc(reflog.Commits, func(commit LostCommit) bool { return commit.Hash == lost }) {
		t.Fatalf("reflog: %+v, %v", reflog, err)
	}
}
