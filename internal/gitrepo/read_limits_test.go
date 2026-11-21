// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// 只读预览输出溢出后必须立即结束 Git 进程树。
func TestRunCancelsGitAfterOutputOverflow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell alias fixture requires a POSIX shell")
	}
	_, dir := fixture(t)
	ctx := context.Background()
	flood := []string{"-c", "alias.flood=!head -c 5000000 /dev/zero; sleep 20", "flood"}
	start := time.Now()
	output, truncated, err := runCapped(ctx, dir, "", 30*time.Second, 1000, flood...)
	if err != nil || !truncated || len(output) != 1000 {
		t.Fatalf("capped output: %d bytes, truncated=%t, err=%v", len(output), truncated, err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("overflowing git was not cancelled early: %v", elapsed)
	}
	// 调用方取消仍按失败返回，不能被当作截断成功。
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, truncated, err := runCapped(cancelled, dir, "", 30*time.Second, 1000, flood...); err == nil || truncated {
		t.Fatalf("cancelled capped run: truncated=%t err=%v", truncated, err)
	}
}

func TestRunDrainsOverflowUntilCommitHookFinishes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell alias and hook fixtures require a POSIX shell")
	}
	_, dir := fixture(t)
	git(t, dir, "config", "core.hooksPath", ".git/hooks")
	hook := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'done\\n' > hook-complete\n"), 0700); err != nil {
		t.Fatal(err)
	}
	output, err := run(context.Background(), dir, "", 30*time.Second,
		"-c", "alias.flood=!head -c 5000000 /dev/zero && git commit --allow-empty -m overflow", "flood")
	if output != "" || !errors.Is(err, errOutputTooLarge) {
		t.Fatalf("overflow output = %d bytes, err = %v", len(output), err)
	}
	if marker, err := os.ReadFile(filepath.Join(dir, "hook-complete")); err != nil || string(marker) != "done\n" {
		t.Fatalf("post-commit hook did not finish: %q, %v", marker, err)
	}
	if subject := git(t, dir, "log", "-1", "--format=%s"); subject != "overflow" {
		t.Fatalf("commit subject = %q", subject)
	}
}

func TestRunPreservesExitStatusAfterOutputOverflow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell alias fixture requires a POSIX shell")
	}
	_, dir := fixture(t)
	_, err := run(context.Background(), dir, "", 30*time.Second,
		"-c", "alias.flood=!head -c 5000000 /dev/zero && exit 23", "flood")
	if !exitCode(err, 23) {
		t.Fatalf("overflowing command did not return its exit status: %T", err)
	}
}

// 完整 status 输出超过 4 MiB 时仓库仍能读取快照，而不是因输出上限无法打开。
func TestSnapshotStreamsStatusBeyondOutputLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("creates thousands of files")
	}
	repo, dir := fixture(t)
	git(t, dir, "config", "--local", "core.longpaths", "true")
	write(t, dir, "tracked.txt", "base\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	// 长目录名让每条未跟踪记录约 730 字节，少量文件即可超过上限。
	parent := filepath.Join(strings.Repeat("a", 240), strings.Repeat("b", 240), strings.Repeat("c", 240))
	if err := os.MkdirAll(filepath.Join(dir, parent), 0755); err != nil {
		t.Fatal(err)
	}
	const count = 6000
	for index := range count {
		if err := os.WriteFile(filepath.Join(dir, parent, fmt.Sprintf("f%05d", index)), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := repo.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Files) != count || snapshot.Branch != "main" || snapshot.Head == "" {
		t.Fatalf("snapshot: %d files, branch %q, head %q", len(snapshot.Files), snapshot.Branch, snapshot.Head)
	}
	size := 0
	for _, file := range snapshot.Files {
		size += len(file.Path) + 3
	}
	if size <= outputLimit {
		t.Fatalf("fixture status output %d bytes does not exceed the limit", size)
	}
}

// 流式解析在任意分块边界下都与整段解析一致，并保留重命名记录的完整性检查。
func TestStatusStreamMatchesWholeParse(t *testing.T) {
	output := strings.Join([]string{
		"# branch.oid 0123456789012345678901234567890123456789",
		"# branch.head main",
		"# branch.upstream origin/main",
		"# branch.ab +2 -1",
		"1 .M N... 100644 100644 100644 aaaa bbbb plain file.txt",
		"2 R. N... 100644 100644 100644 aaaa bbbb R100 new name.txt",
		"old name.txt",
		"u UU N... 100644 100644 100644 100644 aaaa bbbb cccc conflict.txt",
		"? untracked dir/file",
		"! ignored",
	}, "\x00") + "\x00"
	wantBranch, wantFiles, err := parseStatus(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []int{1, 3, 17, len(output)} {
		parser := statusParser{files: []File{}}
		writer := nulRecordWriter{record: parser.record}
		for start := 0; start < len(output); start += chunk {
			if _, err := writer.Write([]byte(output[start:min(start+chunk, len(output))])); err != nil {
				t.Fatal(chunk, err)
			}
		}
		if err := writer.flush(); err != nil {
			t.Fatal(chunk, err)
		}
		branch, files, err := parser.finish()
		if err != nil || branch != wantBranch || !reflect.DeepEqual(files, wantFiles) {
			t.Fatalf("chunk %d: %+v %+v %v\nwant %+v %+v", chunk, branch, files, err, wantBranch, wantFiles)
		}
	}
	if len(wantFiles) != 4 || wantFiles[1].OriginalPath != "old name.txt" || wantBranch.upstream != "origin/main" {
		t.Fatalf("unexpected parse: %+v %+v", wantBranch, wantFiles)
	}
	unknownCounts, unknownFiles, err := parseStatus(strings.Join([]string{
		"# branch.oid 0123456789012345678901234567890123456789",
		"# branch.head main",
		"# branch.upstream origin/main",
		"# branch.ab +? -?",
		"",
	}, "\x00"))
	if err != nil || unknownCounts.head == "" || unknownCounts.branch != "main" || unknownCounts.upstream != "" ||
		unknownCounts.ahead != 0 || unknownCounts.behind != 0 || len(unknownFiles) != 0 {
		t.Fatalf("unknown ahead/behind counts: %+v %+v %v", unknownCounts, unknownFiles, err)
	}
	for _, broken := range []string{
		"2 R. N... 100644 100644 100644 aaaa bbbb R100 new\x00",
		"2 R. N... 100644 100644 100644 aaaa bbbb R100 new\x00\x00",
		"# branch.ab +? -1\x00",
		"x unknown\x00",
	} {
		if _, _, err := parseStatus(broken); err == nil {
			t.Fatalf("accepted malformed status %q", broken)
		}
	}
}

func TestSingleFileDiffsTruncateBeyondOutputLimit(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	git(t, dir, "commit", "--allow-empty", "-m", "empty")
	base := git(t, dir, "rev-parse", "HEAD")
	var content strings.Builder
	for index := 0; content.Len() <= outputLimit+(1<<20); index++ {
		fmt.Fprintf(&content, "line %07d 多字节内容\n", index)
	}
	write(t, dir, "big.txt", content.String())
	write(t, dir, "small.txt", "small\n")
	git(t, dir, "add", ".")
	check := func(name string, diff Diff, err error) {
		t.Helper()
		if err != nil || !diff.Truncated || len(diff.Text) > outputLimit || !strings.HasSuffix(diff.Text, "\n") ||
			!strings.HasPrefix(diff.Text, "diff --git ") {
			t.Fatalf("%s: truncated=%t length=%d err=%v", name, diff.Truncated, len(diff.Text), err)
		}
	}
	staged, err := repo.Diff(ctx, "big.txt", true)
	check("staged", staged, err)
	if staged.New == nil || !staged.New.Truncated {
		t.Fatalf("large index side should stay truncated: %+v", staged.New)
	}
	git(t, dir, "commit", "-m", "big")
	target := git(t, dir, "rev-parse", "HEAD")
	commitDiff, err := repo.CommitFileDiff(ctx, target, "big.txt")
	check("commit", commitDiff, err)
	comparison, err := repo.Compare(ctx, base, target, false)
	if err != nil {
		t.Fatal(err)
	}
	compareDiff, err := repo.CompareFileDiff(ctx, comparison.From, comparison.Target, "big.txt")
	check("compare", compareDiff, err)
	// 小文件不受影响。
	small, err := repo.CommitFileDiff(ctx, target, "small.txt")
	if err != nil || small.Truncated || !strings.Contains(small.Text, "+small") {
		t.Fatalf("small diff: %+v, %v", small, err)
	}
}

// 暂存差异超过通用输出上限时仍须识别为 ErrCommitContextTooLarge。
func TestCommitContextTooLargeBeyondOutputLimit(t *testing.T) {
	repo, dir := fixture(t)
	write(t, dir, "huge.txt", strings.Repeat("huge content line\n", (outputLimit/18)+1000))
	git(t, dir, "add", ".")
	result, err := repo.CommitContext(context.Background())
	if !errors.Is(err, ErrCommitContextTooLarge) || result.Token == "" || result.Diff != "" {
		t.Fatalf("commit context: token=%q diff=%d err=%v", result.Token, len(result.Diff), err)
	}
}

// 单次 log 读取的文件列表与父提交须与 diff-tree、rev-list 完全一致，并拒绝非提交对象。
func TestCommitFilesMatchDiffTreeAndRejectNonCommits(t *testing.T) {
	repo, dir, commits := cacheFixture(t)
	ctx := context.Background()
	git(t, dir, "commit", "--allow-empty", "-m", "empty")
	commits = append(commits, git(t, dir, "rev-parse", "HEAD"))
	for _, hash := range commits {
		files, parents, err := repo.commitFiles(ctx, hash)
		if err != nil {
			t.Fatal(hash, err)
		}
		output := git(t, dir, "diff-tree", "--root", "--diff-merges=first-parent", "--no-commit-id",
			"--name-status", "-r", "-z", "--find-renames", hash, "--")
		want, err := parseCommitFiles(output)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(files, want) {
			t.Fatalf("%s files:\n got %+v\nwant %+v", hash, files, want)
		}
		if wantParents := strings.Fields(git(t, dir, "rev-list", "--parents", "-n", "1", hash)); !slices.Equal(parents, wantParents) {
			t.Fatalf("%s parents = %v, want %v", hash, parents, wantParents)
		}
	}
	if files, _, err := repo.commitFiles(ctx, commits[len(commits)-1]); err != nil || len(files) != 0 {
		t.Fatalf("empty commit files: %+v, %v", files, err)
	}
	git(t, dir, "tag", "-a", "annotated", "-m", "tag", commits[1])
	for _, object := range []string{
		git(t, dir, "rev-parse", "annotated"),
		git(t, dir, "rev-parse", commits[1]+"^{tree}"),
		git(t, dir, "rev-parse", commits[1]+":a.txt"),
	} {
		if _, err := repo.CommitFiles(ctx, object); err == nil || !strings.Contains(err.Error(), "未指向提交对象") {
			t.Fatalf("%s accepted as commit: %v", object, err)
		}
	}
	if _, err := repo.CommitFiles(ctx, strings.Repeat("0", len(commits[0]))); err == nil {
		t.Fatal("missing object accepted")
	}
}

// 暂存检查用一次 cat-file --batch 读取小对象样本，结果与逐个读取一致，大对象仍单独读取。
func TestInspectStagedFilesBatchesSamples(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize 12\n"
	files := map[string]string{
		"text.txt":          "plain text\n",
		"copy.txt":          "plain text\n",
		"empty.txt":         "",
		"late-nul.bin":      strings.Repeat("x", inspectionSampleLimit) + "\x00",
		"early-nul.bin":     "\x00binary",
		"pointer.bin":       pointer,
		"nested/deep/a.txt": strings.Repeat("y", inspectionSampleLimit*3),
		"large.bin":         "\x00" + strings.Repeat("z", inspectionBatchLimit+1),
		"large-copy.bin":    "\x00" + strings.Repeat("z", inspectionBatchLimit+1),
	}
	for name, content := range files {
		write(t, dir, name, content)
	}
	testSymlink(t, "text.txt", filepath.Join(dir, "link"))
	git(t, dir, "add", ".")
	files[".gitattributes"] = "copy.txt -diff filter=lfs\nlarge-copy.bin diff\n"
	write(t, dir, ".gitattributes", files[".gitattributes"])
	git(t, dir, "add", ".gitattributes")
	reset, calls := gitCallLog(t)
	reset()
	report, err := repo.InspectStagedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := calls("cat-file --batch"); got != 1 {
		t.Fatalf("batch reads = %d, want 1", got)
	}
	if got := calls("cat-file blob"); got != 1 {
		t.Fatalf("single blob reads = %d, want 1 for the large object", got)
	}
	if got := calls("ls-files"); got != 0 {
		t.Fatalf("ls-files calls = %d, want 0", got)
	}
	got := map[string]FileInspection{}
	paths := make([]string, 0, len(report.Files))
	for _, file := range report.Files {
		got[file.Path] = file
		paths = append(paths, file.Path)
	}
	if !slices.IsSorted(paths) {
		t.Fatalf("inspected paths out of order: %v", paths)
	}
	if len(got) != len(files)+1 {
		t.Fatalf("inspected %d files: %+v", len(got), report.Files)
	}
	for name, content := range files {
		file := got[name]
		oid := strings.Fields(git(t, dir, "ls-files", "--stage", "--", name))[1]
		binary := strings.Contains(content[:min(len(content), inspectionSampleLimit)], "\x00")
		if name == "copy.txt" {
			binary = true
		} else if name == "large-copy.bin" {
			binary = false
		}
		if file.Size != int64(len(content)) || file.Binary != binary || file.Fingerprint != oid ||
			file.Pointer != (name == "pointer.bin") || file.LFS != (name == "copy.txt") {
			t.Fatalf("%s: %+v (binary want %t)", name, file, binary)
		}
	}
	if link := got["link"]; link.Size != int64(len("text.txt")) || link.Binary || link.Pointer {
		t.Fatalf("symlink: %+v", link)
	}
}

func TestBlobBatchWriterHandlesFragmentsAndRejectsMismatch(t *testing.T) {
	first, second := strings.Repeat("1", 40), strings.Repeat("2", 40)
	firstRecord := first + " blob 3\na\x00b\n"
	body := strings.Repeat("s", inspectionSampleLimit+9) + "\x00"
	secondRecord := second + " blob " + fmt.Sprint(len(body)) + "\n" + body + "\n"
	stream := firstRecord + secondRecord
	for _, chunk := range []int{1, 7, len(stream)} {
		writer := blobBatchWriter{oids: []string{first, second}, sizes: []int64{3, int64(len(body))},
			contents: map[string]inspectionBlobContent{}, cancel: func() {}}
		for start := 0; start < len(stream); start += chunk {
			if _, err := writer.Write([]byte(stream[start:min(start+chunk, len(stream))])); err != nil {
				t.Fatal(chunk, err)
			}
		}
		if writer.index != 2 || len(writer.sample) != 0 || len(writer.contents) != 2 ||
			!writer.contents[first].binary || writer.contents[second].binary {
			t.Fatalf("chunk %d: index %d sample size %d contents %+v", chunk, writer.index, len(writer.sample), writer.contents)
		}
	}
	for _, broken := range []string{
		first + " missing\n",
		first + " blob 4\nabcd\n",
		first + " blob 3\nabcX",
		second + " blob 3\nabc\n",
	} {
		cancelled := false
		writer := blobBatchWriter{oids: []string{first}, sizes: []int64{3}, contents: map[string]inspectionBlobContent{},
			cancel: func() { cancelled = true }}
		if _, err := writer.Write([]byte(broken)); err == nil || !cancelled {
			t.Fatalf("accepted %q (cancelled=%t)", broken, cancelled)
		}
	}
}

func TestBlobBatchWriterClassifiesAndReleasesEachSample(t *testing.T) {
	first, second := strings.Repeat("1", 40), strings.Repeat("2", 40)
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize 12\n"
	writer := blobBatchWriter{oids: []string{first, second}, sizes: []int64{int64(len(pointer)), int64(len(pointer) + 1)},
		contents: map[string]inspectionBlobContent{}, cancel: func() {}}
	if _, err := fmt.Fprintf(&writer, "%s blob %d\n%s\n", first, len(pointer), pointer); err != nil {
		t.Fatal(err)
	}
	if got := writer.contents[first]; !got.pointer || got.binary || len(writer.sample) != 0 {
		t.Fatalf("first blob should already be classified: %+v, sample size %d", got, len(writer.sample))
	}
	if _, err := fmt.Fprintf(&writer, "%s blob %d\n%s\x00\n", second, len(pointer)+1, pointer); err != nil {
		t.Fatal(err)
	}
	if got := writer.contents[second]; got.pointer || !got.binary || len(writer.sample) != 0 {
		t.Fatalf("second blob should have independent content: %+v, sample size %d", got, len(writer.sample))
	}
}

// 常见布局直接解析 Git 目录，结果须与 rev-parse 指向同一目录；检查嵌套路径时不再启动 rev-parse。
func TestLocalMetadataDirectoriesMatchRevParse(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	git(t, dir, "commit", "--allow-empty", "-m", "base")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, dir, "worktree", "add", "-b", "linked", linked)
	relative := t.TempDir()
	git(t, relative, "init", "--separate-git-dir", filepath.Join(relative, "meta"), filepath.Join(relative, "work"))
	if err := os.WriteFile(filepath.Join(relative, "work", ".git"), []byte("gitdir: ../meta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, linked} {
		target, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		local, ok := localMetadataDirectories(target.Path)
		if !ok {
			t.Fatalf("%s: local parse failed", path)
		}
		want, err := target.metadataDirectories(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for index := range want {
			if !worktreeSamePath(local[index], want[index]) {
				t.Fatalf("%s: metadata %d = %q, want %q", path, index, local[index], want[index])
			}
		}
	}
	worktree, err := Open(ctx, filepath.Join(relative, "work"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := localMetadataDirectories(worktree.Path); ok {
		t.Fatal("parent path component should fall back to Git")
	}
	actual, err := worktree.gitDirectory(ctx)
	if err != nil || !worktreeSamePath(actual, filepath.Join(relative, "meta")) {
		t.Fatalf("relative gitfile resolved to %q: %v", actual, err)
	}
	// 无法确定的 .git 文件交给 Git 判断，不猜测。
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: missing\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := localMetadataDirectories(broken); ok {
		t.Fatal("missing gitdir target should fall back to Git")
	}
	for _, name := range []string{"one/a.txt", "one/b.txt", "two/deep/c.txt"} {
		write(t, dir, name, name)
	}
	reset, calls := gitCallLog(t)
	reset()
	report, err := repo.InspectFiles(ctx, []string{"one/a.txt", "one/b.txt", "two/deep/c.txt"})
	if err != nil || len(report.Files) != 3 {
		t.Fatalf("inspect nested files: %+v, %v", report, err)
	}
	if got := calls("rev-parse"); got != 0 {
		t.Fatalf("rev-parse calls = %d, want 0", got)
	}
}

// 读取冲突与其他只读入口一致，等待仓库锁时响应取消。
func TestConflictWaitsForLockWithContext(t *testing.T) {
	repo, _ := fixture(t)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := repo.Conflict(ctx, "file.txt"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("conflict while busy: %v", err)
	}
}

// 用户配置不能改变单次 log 读取的结果；SHA-256 仓库同样适用。
func TestCommitFilesIgnoreUserConfigAndSupportSHA256(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			git(t, dir, "init", "-b", "main", "--object-format="+format)
			git(t, dir, "config", "user.name", "Test User")
			git(t, dir, "config", "user.email", "test@example.test")
			git(t, dir, "config", "commit.gpgsign", "false")
			write(t, dir, "a.txt", strings.Repeat("same line\n", 20))
			write(t, dir, "z.txt", "z\n")
			git(t, dir, "add", ".")
			git(t, dir, "commit", "-m", "root")
			if err := os.Mkdir(filepath.Join(dir, "sub"), 0755); err != nil {
				t.Fatal(err)
			}
			git(t, dir, "mv", "a.txt", "sub/b.txt")
			write(t, dir, "z.txt", "changed\n")
			git(t, dir, "add", ".")
			git(t, dir, "commit", "-m", "rename")
			hash := git(t, dir, "rev-parse", "HEAD")
			if len(hash) != map[string]int{"sha1": 40, "sha256": 64}[format] {
				t.Fatalf("unexpected hash %q", hash)
			}
			git(t, dir, "notes", "add", "-m", "note", hash)
			git(t, dir, "tag", "-a", "annotated", "-m", "tag", hash)
			for key, value := range map[string]string{
				"log.showSignature": "true", "log.decorate": "full", "log.showRoot": "false",
				"log.abbrevCommit": "true", "diff.renames": "copies", "diff.relative": "true",
				"color.ui": "always", "color.diff": "always", "diff.orderFile": "order",
				"diff.ignoreSubmodules": "all", "format.pretty": "fuller",
			} {
				git(t, dir, "config", key, value)
			}
			write(t, dir, "order", "z.txt\n")
			repo, err := Open(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			files, err := repo.CommitFiles(context.Background(), hash)
			want := []CommitFile{{Path: "sub/b.txt", Status: "R", OriginalPath: "a.txt"}, {Path: "z.txt", Status: "M"}}
			if err != nil || !reflect.DeepEqual(files, want) {
				t.Fatalf("files = %+v, %v", files, err)
			}
			if _, err := repo.CommitFiles(context.Background(), git(t, dir, "rev-parse", "annotated")); err == nil {
				t.Fatal("annotated tag accepted as commit")
			}
			diff, err := repo.CommitFileDiff(context.Background(), hash, "z.txt")
			if err != nil || diff.Old == nil || diff.Old.Text != "z\n" || diff.New.Text != "changed\n" {
				t.Fatalf("diff = %+v, %v", diff, err)
			}
		})
	}
}
