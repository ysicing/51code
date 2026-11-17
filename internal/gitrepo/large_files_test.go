// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func inspectionByPath(t *testing.T, report FileInspectionReport) map[string]FileInspection {
	t.Helper()
	files := make(map[string]FileInspection, len(report.Files))
	for _, file := range report.Files {
		if _, exists := files[file.Path]; exists {
			t.Fatalf("duplicate inspection: %s", file.Path)
		}
		if file.Fingerprint == "" {
			t.Fatalf("missing fingerprint: %s", file.Path)
		}
		files[file.Path] = file
	}
	return files
}

func TestInspectFilesSelectedChangedPaths(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, "unchanged.txt", "base\n")
	write(t, dir, "deleted.txt", "deleted\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	if err := os.Remove(filepath.Join(dir, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	name := portableTestPath("目录/[literal]\t\n.txt", "目录/[literal] 文件.txt")
	write(t, dir, name, "hello\n")
	write(t, dir, "excluded.bin", "\x00excluded")
	report, err := repo.InspectFiles(ctx, []string{name, "deleted.txt", name})
	if err != nil {
		t.Fatal(err)
	}
	files := inspectionByPath(t, report)
	if len(files) != 1 || files[name].Size != 6 || files[name].Binary {
		t.Fatalf("selected worktree inspection: %+v", report)
	}
	token, err := runDigest(ctx, repo.Path, 30*time.Second, "ls-files", "--stage", "-z")
	if err != nil || report.Token != token {
		t.Fatalf("inspection token: %q, %v", report.Token, err)
	}
	for _, paths := range [][]string{nil, {"unchanged.txt"}, {"../escape"}, {".git/config"}} {
		if _, err := repo.InspectFiles(ctx, paths); err == nil {
			t.Fatalf("accepted invalid selection: %q", paths)
		}
	}
}

func TestInspectFilesBinaryAttributesAndLFS(t *testing.T) {
	repo, dir := fixture(t)
	write(t, dir, ".gitattributes", "*.binary -diff\n*.text diff\n*.driver diff=opaque\n*.readable diff=readable\n*.lfs filter=lfs\n")
	git(t, dir, "config", "diff.opaque.binary", "true")
	git(t, dir, "config", "diff.readable.binary", "false")
	git(t, dir, "config", "diff.opaque.command", "must-not-run")
	git(t, dir, "config", "diff.opaque.textconv", "must-not-run")
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize 999999999\n"
	tests := []struct {
		path, content string
		binary, lfs   bool
		pointer       bool
	}{
		{"plain.txt", "plain\n", false, false, false},
		{"nul-byte.bin", "a\x00b", true, false, false},
		{"invalid.bin", "a\xffb", false, false, false},
		{"forced.binary", "ordinary text", true, false, false},
		{"forced.text", "\x00binary", false, false, false},
		{"forced.driver", "ordinary text", true, false, false},
		{"forced.readable", "\x00binary", false, false, false},
		{"ordinary.lfs", "\x00payload", true, true, false},
		{"pointer.lfs", pointer, false, true, true},
		{"pointer.txt", pointer, false, false, true},
		{"trailing.txt", pointer + strings.Repeat("x", 9000), false, false, false},
	}
	paths := make([]string, 0, len(tests))
	for _, test := range tests {
		write(t, dir, test.path, test.content)
		paths = append(paths, test.path)
	}
	report, err := repo.InspectFiles(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	files := inspectionByPath(t, report)
	for _, test := range tests {
		file, exists := files[test.path]
		if !exists || file.Size != int64(len(test.content)) || file.Binary != test.binary ||
			file.LFS != test.lfs || file.Pointer != test.pointer {
			t.Errorf("%s: %+v", test.path, file)
		}
	}
}

func TestInspectFilesBoundedSampleAndMetadataSize(t *testing.T) {
	repo, dir := fixture(t)
	path := filepath.Join(dir, "large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	size := int64(3) << 30
	sample := bytes.Repeat([]byte("a"), 8192)
	if _, err := file.Write(sample); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := repo.InspectFiles(context.Background(), []string{"large.bin"})
	if err != nil || len(report.Files) != 1 || report.Files[0].Size != size || report.Files[0].Binary {
		t.Fatalf("bounded worktree sample: %+v, %v", report, err)
	}
}

func TestInspectFilesSymlinkDoesNotReadTarget(t *testing.T) {
	repo, dir := fixture(t)
	outside := t.TempDir()
	write(t, outside, "secret", "\x00outside secret")
	target := filepath.Join(outside, "secret")
	testSymlink(t, target, filepath.Join(dir, "link"))
	info, err := os.Lstat(filepath.Join(dir, "link"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := repo.InspectFiles(context.Background(), []string{"link"})
	if err != nil || len(report.Files) != 1 || report.Files[0].Size != info.Size() ||
		report.Files[0].Binary || report.Files[0].Pointer {
		t.Fatalf("symlink inspection read target: %+v, %v", report, err)
	}
	git(t, dir, "add", "link")
	staged, err := repo.InspectStagedFiles(context.Background())
	if err != nil || len(staged.Files) != 1 || staged.Files[0].Size != int64(len(target)) || staged.Files[0].Binary {
		t.Fatalf("staged symlink inspection: %+v, %v", staged, err)
	}
}

func TestInspectFilesReplacedAncestorDoesNotReadOutside(t *testing.T) {
	repo, dir := fixture(t)
	write(t, dir, "nested/tracked.txt", "base\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	if err := os.Remove(filepath.Join(dir, "nested", "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "nested")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	write(t, outside, "tracked.txt", "\x00outside secret")
	testSymlink(t, outside, filepath.Join(dir, "nested"))
	report, err := repo.InspectFiles(context.Background(), []string{"nested/tracked.txt"})
	if err != nil || len(report.Files) != 0 {
		t.Fatalf("followed replaced ancestor: %+v, %v", report, err)
	}
}

func TestInspectStagedFilesReadsIndexAndCachedAttributes(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, "unchanged.txt", "base\n")
	write(t, dir, "deleted.txt", "base\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	write(t, dir, ".gitattributes", "*.bin -diff\n*.lfs filter=lfs\n")
	write(t, dir, "index.bin", "index text\n")
	write(t, dir, "auto.txt", "\x00index bytes")
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("b", 64) + "\nsize 123456789\n"
	write(t, dir, "pointer.lfs", pointer)
	git(t, dir, "add", ".gitattributes", "index.bin", "auto.txt", "pointer.lfs")
	git(t, dir, "rm", "deleted.txt")
	write(t, dir, ".gitattributes", "*.bin diff\n*.lfs -filter\n")
	write(t, dir, "index.bin", "\x00worktree replacement with another size")
	write(t, dir, "auto.txt", "worktree text\n")
	write(t, dir, "pointer.lfs", "\x00smudged payload")
	write(t, dir, "untracked.txt", "\x00not staged")
	report, err := repo.InspectStagedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files := inspectionByPath(t, report)
	if len(files) != 4 || files["index.bin"].Size != int64(len("index text\n")) || !files["index.bin"].Binary ||
		!files["auto.txt"].Binary || !files["pointer.lfs"].LFS || !files["pointer.lfs"].Pointer ||
		files["pointer.lfs"].Size != int64(len(pointer)) {
		t.Fatalf("index inspection: %+v", report)
	}
	for path, file := range files {
		if want := git(t, dir, "rev-parse", ":"+path); file.Fingerprint != want {
			t.Errorf("index fingerprint %s: %q, want %q", path, file.Fingerprint, want)
		}
	}
	token, err := repo.CommitToken(ctx)
	if err != nil || report.Token != token {
		t.Fatalf("staged token: %q, %v", report.Token, err)
	}
}

func TestInspectStagedFilesUnbornAndLargeBlob(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	empty, err := repo.InspectStagedFiles(ctx)
	if err != nil || len(empty.Files) != 0 || empty.Token == "" {
		t.Fatalf("empty unborn inspection: %+v, %v", empty, err)
	}
	write(t, dir, "large.txt", strings.Repeat("a", (4<<20)+8192)+"\x00")
	git(t, dir, "add", "large.txt")
	if err := os.Remove(filepath.Join(dir, "large.txt")); err != nil {
		t.Fatal(err)
	}
	report, err := repo.InspectStagedFiles(ctx)
	if err != nil || len(report.Files) != 1 || report.Files[0].Size != (4<<20)+8193 || report.Files[0].Binary {
		t.Fatalf("bounded staged inspection: %+v, %v", report, err)
	}
}

func TestInspectStagedFilesRenameAndGitlink(t *testing.T) {
	repo, dir := fixture(t)
	write(t, dir, "old.txt", "\x00payload")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	git(t, dir, "mv", "old.txt", "new.txt")
	head := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
	report, err := repo.InspectStagedFiles(context.Background())
	if err != nil || len(report.Files) != 1 || report.Files[0].Path != "new.txt" || !report.Files[0].Binary {
		t.Fatalf("rename/gitlink inspection: %+v, %v", report, err)
	}
}

func TestStageInspectedRejectsStaleWorktreeWithoutIndexMutation(t *testing.T) {
	for _, change := range []string{"content", "size", "delete", "attributes", "replacement"} {
		t.Run(change, func(t *testing.T) {
			repo, dir := fixture(t)
			ctx := context.Background()
			write(t, dir, "file.txt", "initial\n")
			expected, err := repo.InspectFiles(ctx, []string{"file.txt"})
			if err != nil {
				t.Fatal(err)
			}
			indexPath := filepath.Join(dir, ".git", "index")
			before, beforeErr := os.ReadFile(indexPath)
			switch change {
			case "content":
				write(t, dir, "file.txt", "changed\n")
			case "size":
				write(t, dir, "file.txt", "larger changed contents\n")
			case "delete":
				if err := os.Remove(filepath.Join(dir, "file.txt")); err != nil {
					t.Fatal(err)
				}
			case "attributes":
				write(t, dir, ".gitattributes", "*.txt -diff\n")
			case "replacement":
				info, err := os.Stat(filepath.Join(dir, "file.txt"))
				if err != nil {
					t.Fatal(err)
				}
				write(t, dir, "file.txt", "changed\n")
				if err := os.Chtimes(filepath.Join(dir, "file.txt"), info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := repo.StageInspected(ctx, []string{"file.txt"}, expected); err == nil {
				t.Fatal("staged stale inspection")
			}
			after, afterErr := os.ReadFile(indexPath)
			if !bytes.Equal(before, after) || os.IsNotExist(beforeErr) != os.IsNotExist(afterErr) {
				t.Fatal("stale stage changed index")
			}
		})
	}
}

func TestStageInspectedPreservesExternallyPartiallyStagedIndex(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, "file.txt", "first\nkeep\nsecond\n")
	git(t, dir, "add", "file.txt")
	git(t, dir, "commit", "-m", "base")
	worktree := "changed first\nkeep\nchanged second\n"
	write(t, dir, "file.txt", worktree)
	expected, err := repo.InspectFiles(ctx, []string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	partialDir := t.TempDir()
	write(t, partialDir, "partial.txt", "changed first\nkeep\nsecond\n")
	oid := git(t, dir, "hash-object", "-w", "--", filepath.Join(partialDir, "partial.txt"))
	git(t, dir, "update-index", "--cacheinfo", "100644,"+oid+",file.txt")
	current, err := repo.InspectFiles(ctx, []string{"file.txt"})
	if err != nil || !reflect.DeepEqual(current.Files, expected.Files) || current.Token == expected.Token {
		t.Fatalf("partial staging fixture: expected %+v, current %+v, %v", expected, current, err)
	}
	indexPath := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := repo.StageInspected(ctx, []string{"file.txt"}, expected)
	if err == nil || staged.Token != "" || len(staged.Files) != 0 {
		t.Fatalf("accepted stale index inspection: %+v, %v", staged, err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected staging changed index: %v", err)
	}
	if got := git(t, dir, "rev-parse", ":file.txt"); got != oid {
		t.Fatalf("overwrote external partial staging: %q, want %q", got, oid)
	}
	content, err := os.ReadFile(filepath.Join(dir, "file.txt"))
	if err != nil || string(content) != worktree {
		t.Fatalf("rejected staging changed worktree: %q, %v", content, err)
	}
}

func TestStageInspectedRequiresIndexToken(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, "file.txt", "new content\n")
	expected, err := repo.InspectFiles(ctx, []string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	expected.Token = ""
	if _, err := repo.StageInspected(ctx, []string{"file.txt"}, expected); err == nil {
		t.Fatal("accepted missing index inspection token")
	}
	if got := git(t, dir, "ls-files", "--stage"); got != "" {
		t.Fatalf("missing token mutated index: %q", got)
	}
}

func TestInspectFilesFingerprintExcludesAttributes(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, "file.bin", "\x00payload")
	expected, err := repo.InspectFiles(ctx, []string{"file.bin"})
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, ".gitattributes", "file.bin filter=lfs diff\n")
	current, err := repo.InspectFiles(ctx, []string{"file.bin"})
	if err != nil || len(expected.Files) != 1 || len(current.Files) != 1 {
		t.Fatalf("attribute inspection: %+v, %+v, %v", expected, current, err)
	}
	before, after := expected.Files[0], current.Files[0]
	if before.Fingerprint != after.Fingerprint || before.Size != after.Size {
		t.Fatalf("attributes changed content fingerprint: %+v, %+v", before, after)
	}
	if !before.Binary || before.LFS || after.Binary || !after.LFS {
		t.Fatalf("attribute classifications did not change: %+v, %+v", before, after)
	}
	if _, err := repo.StageInspected(ctx, []string{"file.bin"}, expected); err == nil {
		t.Fatal("ordinary staging ignored changed attribute classifications")
	}
	if got := git(t, dir, "ls-files", "--stage"); got != "" {
		t.Fatalf("rejected attribute change mutated index: %q", got)
	}
}

func TestStageInspectedStagesSelectionAndDeletion(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, "deleted.txt", "base\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	if err := os.Remove(filepath.Join(dir, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "[literal].txt", "selected\n")
	write(t, dir, "other.txt", "other\n")
	paths := []string{"[literal].txt", "deleted.txt"}
	report, err := repo.InspectFiles(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := repo.StageInspected(ctx, paths, report)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Files) != 1 || staged.Files[0].Path != "[literal].txt" ||
		staged.Files[0].Fingerprint != git(t, dir, "rev-parse", ":[literal].txt") || staged.Token == "" {
		t.Fatalf("stage did not return actual index report: %+v", staged)
	}
	if got := git(t, dir, "show", ":[literal].txt"); got != "selected" {
		t.Fatalf("selected file not staged: %q", got)
	}
	if got := git(t, dir, "diff", "--cached", "--name-only"); strings.Contains(got, "other.txt") ||
		!strings.Contains(got, "deleted.txt") {
		t.Fatalf("incorrect staging scope: %q", got)
	}
}

func TestStageInspectedRejectsDifferentSelectionAndReappearedDeletion(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, "one.txt", "one\n")
	write(t, dir, "two.txt", "two\n")
	expected, err := repo.InspectFiles(ctx, []string{"one.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StageInspected(ctx, []string{"one.txt", "two.txt"}, expected); err == nil {
		t.Fatal("staged a file outside inspection")
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	if err := os.Remove(filepath.Join(dir, "one.txt")); err != nil {
		t.Fatal(err)
	}
	deleted, err := repo.InspectFiles(ctx, []string{"one.txt"})
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "one.txt", "\x00reappeared")
	if _, err := repo.StageInspected(ctx, []string{"one.txt"}, deleted); err == nil {
		t.Fatal("staged file recreated after deletion inspection")
	}
}

func TestStageInspectedRefusesAcknowledgmentAfterFilterChangesWorktree(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, ".gitattributes", "file.txt filter=change-worktree\n")
	write(t, dir, "file.txt", "inspected\n")
	git(t, dir, "config", "filter.change-worktree.clean", "cat; printf rewritten > file.txt")
	report, err := repo.InspectFiles(ctx, []string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	staged, err := repo.StageInspected(ctx, []string{"file.txt"}, report)
	if err == nil || staged.Token != "" || len(staged.Files) != 0 {
		t.Fatalf("acknowledged changed worktree: %+v, %v", staged, err)
	}
	if got := git(t, dir, "show", ":file.txt"); got != "inspected" {
		t.Fatalf("filter fixture did not stage original content: %q", got)
	}
	content, err := os.ReadFile(filepath.Join(dir, "file.txt"))
	if err != nil || string(content) != "rewritten" {
		t.Fatalf("filter fixture did not change worktree: %q, %v", content, err)
	}
}

func TestStageInspectedPermitsUnrelatedUnmergedFiles(t *testing.T) {
	for _, rewrite := range []bool{false, true} {
		name := "unchanged worktree"
		if rewrite {
			name = "filter rewrites worktree"
		}
		t.Run(name, func(t *testing.T) {
			repo, dir := conflictFixture(t)
			ctx := context.Background()
			if err := repo.Merge(ctx, "topic"); err == nil {
				t.Fatal("fixture did not produce merge conflict")
			}
			unmerged := git(t, dir, "ls-files", "--unmerged", "-z")
			if unmerged == "" {
				t.Fatal("fixture has no unmerged index entries")
			}
			write(t, dir, "ordinary.txt", "inspected\n")
			if rewrite {
				write(t, dir, ".gitattributes", "ordinary.txt filter=change-worktree\n")
				git(t, dir, "config", "filter.change-worktree.clean", "cat; printf rewritten > ordinary.txt")
			}
			indexPath := filepath.Join(dir, ".git", "index")
			before, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := repo.InspectFiles(ctx, []string{"ordinary.txt"})
			if err != nil || len(expected.Files) != 1 || expected.Files[0].Path != "ordinary.txt" {
				t.Fatalf("unrelated conflict blocked inspection: %+v, %v", expected, err)
			}
			after, err := os.ReadFile(indexPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("worktree inspection changed conflicted index: %v", err)
			}
			token, err := runDigest(ctx, repo.Path, 30*time.Second, "ls-files", "--stage", "-z")
			if err != nil || expected.Token != token {
				t.Fatalf("unmerged worktree token: %q, %v", expected.Token, err)
			}
			staged, err := repo.StageInspected(ctx, []string{"ordinary.txt"}, expected)
			if rewrite && err == nil {
				t.Fatal("unmerged fallback ignored worktree rewrite")
			}
			if !rewrite && err != nil {
				t.Fatalf("unrelated conflict blocked staging: %v", err)
			}
			if staged.Token != "" || len(staged.Files) != 0 {
				t.Fatalf("unmerged staging returned acknowledgment: %+v", staged)
			}
			if got := git(t, dir, "show", ":ordinary.txt"); got != "inspected" {
				t.Fatalf("ordinary file not staged: %q", got)
			}
			if got := git(t, dir, "ls-files", "--unmerged", "-z"); got != unmerged {
				t.Fatal("staging changed unrelated conflict entries")
			}
			if _, err := repo.InspectStagedFiles(ctx); err == nil {
				t.Fatal("staged inspection accepted unmerged index")
			}
			head := git(t, dir, "rev-parse", "HEAD")
			if err := repo.CommitInspected(ctx, "reject unresolved conflict", expected.Token); err == nil {
				t.Fatal("commit accepted unmerged index")
			}
			if got := git(t, dir, "rev-parse", "HEAD"); got != head {
				t.Fatal("rejected commit changed HEAD")
			}
			if _, err := repo.InspectFiles(ctx, []string{"conflict.txt"}); err == nil {
				t.Fatal("worktree inspection accepted selected conflict")
			}
		})
	}
}

func TestCommitInspectedVerifiesIndexAndHead(t *testing.T) {
	for _, change := range []string{"index", "head"} {
		t.Run(change, func(t *testing.T) {
			repo, dir := fixture(t)
			ctx := context.Background()
			write(t, dir, "file.txt", "base\n")
			git(t, dir, "add", ".")
			git(t, dir, "commit", "-m", "base")
			write(t, dir, "file.txt", "staged\n")
			git(t, dir, "add", ".")
			report, err := repo.InspectStagedFiles(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if change == "index" {
				write(t, dir, "file.txt", "changed index\n")
				git(t, dir, "add", ".")
			} else {
				git(t, dir, "commit", "--allow-empty", "-m", "external commit")
			}
			head := git(t, dir, "rev-parse", "HEAD")
			if err := repo.CommitInspected(ctx, "refuse stale", report.Token); err == nil {
				t.Fatal("committed stale inspection")
			}
			if got := git(t, dir, "rev-parse", "HEAD"); got != head {
				t.Fatal("stale commit moved HEAD")
			}
		})
	}
}

func TestCommitInspectedPreservesCommitSemantics(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, "file.txt", "staged\n")
	git(t, dir, "add", ".")
	report, err := repo.InspectStagedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "file.txt", "unstaged\n")
	for _, message := range []string{" ", strings.Repeat("x", (64<<10)+1)} {
		if err := repo.CommitInspected(ctx, message, report.Token); err == nil {
			t.Fatal("accepted invalid commit message")
		}
	}
	if err := repo.CommitInspected(ctx, "message", ""); err == nil {
		t.Fatal("accepted missing token")
	}
	if err := repo.CommitInspected(ctx, "标题\n\n正文", report.Token); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "show", "HEAD:file.txt"); got != "staged" {
		t.Fatalf("committed worktree content: %q", got)
	}
	if got := git(t, dir, "log", "-1", "--format=%B"); got != "标题\n\n正文" {
		t.Fatalf("commit message changed: %q", got)
	}
}

func TestFileInspectionCancellation(t *testing.T) {
	repo, dir := fixture(t)
	write(t, dir, "file.txt", "content")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repo.InspectFiles(ctx, []string{"file.txt"}); err == nil {
		t.Fatal("ignored worktree cancellation")
	}
	if _, err := repo.InspectStagedFiles(ctx); err == nil {
		t.Fatal("ignored staged cancellation")
	}
	if _, err := repo.StageInspected(ctx, []string{"file.txt"}, FileInspectionReport{}); err == nil {
		t.Fatal("ignored stage cancellation")
	}
	if err := repo.CommitInspected(ctx, "message", "token"); err == nil {
		t.Fatal("ignored commit cancellation")
	}
}

func TestLFSPointerRecognition(t *testing.T) {
	oid := "sha256:" + strings.Repeat("a", 64)
	pointer := "version https://git-lfs.github.com/spec/v1\noid " + oid + "\nsize 123\n"
	for _, valid := range []string{
		pointer,
		strings.TrimSuffix(pointer, "\n"),
		strings.ReplaceAll(pointer, "\n", "\r\n"),
		strings.Replace(pointer, "git-lfs.github.com", "hawser.github.com", 1),
		strings.Replace(pointer, "oid ", "ext-0-foo "+oid+"\noid ", 1),
		strings.Replace(pointer, "size 123", "size 0", 1),
	} {
		if !isLFSPointer([]byte(valid)) {
			t.Errorf("rejected pointer: %q", valid)
		}
	}
	for _, invalid := range []string{
		"",
		"prefix\n" + pointer,
		pointer + "payload\n",
		pointer + strings.Repeat("x", 8192),
		strings.Replace(pointer, oid, "sha256:bad", 1),
		strings.Replace(pointer, oid, "sha1:"+strings.Repeat("a", 40), 1),
		strings.Replace(pointer, "size 123", "size -1", 1),
		strings.Replace(pointer, "size 123", "size 9223372036854775808", 1),
		strings.Replace(pointer, "size 123", "size +123", 1),
		strings.Replace(pointer, "size 123", "size nope", 1),
		strings.Replace(pointer, "version ", "version  ", 1),
		strings.Replace(pointer, "spec/v1", "spec/v2", 1),
		strings.Replace(pointer, "size 123\n", "", 1),
		strings.Replace(pointer, "oid ", "ext-0-foo "+oid+"\next-0-bar "+oid+"\noid ", 1),
	} {
		if isLFSPointer([]byte(invalid)) {
			t.Errorf("accepted malformed pointer: %q", invalid)
		}
	}
}

func TestInspectionSampleWriterBoundsAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := inspectionSampleWriter{limit: 8192, cancel: cancel}
	data := bytes.Repeat([]byte("x"), 32768)
	count, err := writer.Write(data)
	if err != nil || count != len(data) || len(writer.sample) != 8192 || ctx.Err() == nil {
		t.Fatalf("sample writer: count %d, size %d, err %v", count, len(writer.sample), err)
	}
	if _, err := writer.Write(data); err != nil || len(writer.sample) != 8192 {
		t.Fatalf("writer exceeded sample limit: %v", err)
	}
}

func TestInspectFilesStableFingerprint(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, "file.txt", "same contents")
	first, err := repo.InspectFiles(ctx, []string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.InspectFiles(ctx, []string{"file.txt"})
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("unchanged fingerprint was unstable: %+v, %+v, %v", first, second, err)
	}
	changedTime := time.Unix(1700000000, 0)
	if err := os.Chtimes(filepath.Join(dir, "file.txt"), changedTime, changedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StageInspected(ctx, []string{"file.txt"}, first); err == nil {
		t.Fatal("staged file with changed metadata")
	}
}
