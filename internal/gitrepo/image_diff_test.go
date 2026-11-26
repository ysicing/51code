// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageDiffVersions(t *testing.T) {
	repo, dir := fixture(t)
	ctx := t.Context()
	const name = "图片 [draft].PNG"
	const renamed = "renamed.png"
	const head, index, work = "\x00head", "\x00index", "\x00work"
	check := func(diff Diff, err error, oldPath, oldRev, oldData, newPath, newRev, newData string) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		side := func(got *FileContent, path, rev, data string) {
			t.Helper()
			if got == nil || got.Path != path || got.Revision != rev || got.ImageFormat != "png" ||
				got.Present != (data != "") || got.ImageData != data || got.Truncated || got.Unsupported {
				t.Fatalf("side for %s at %q = %+v, want data %q", path, rev, got, data)
			}
		}
		side(diff.Old, oldPath, oldRev, oldData)
		side(diff.New, newPath, newRev, newData)
	}
	write(t, dir, name, head)
	diff, err := repo.Diff(ctx, name, false)
	check(diff, err, name, "INDEX", "", name, "WORKTREE", head)
	git(t, dir, "add", "--", name)
	diff, err = repo.Diff(ctx, name, true)
	check(diff, err, name, "HEAD", "", name, "INDEX", head)
	git(t, dir, "commit", "-m", "initial image")
	first := git(t, dir, "rev-parse", "HEAD")
	diff, err = repo.CommitFileDiff(ctx, first, name)
	check(diff, err, name, "", "", name, first, head)

	write(t, dir, name, index)
	git(t, dir, "add", "--", name)
	write(t, dir, name, work)
	diff, err = repo.Diff(ctx, name, true)
	check(diff, err, name, "HEAD", head, name, "INDEX", index)
	diff, err = repo.Diff(ctx, name, false)
	check(diff, err, name, "INDEX", index, name, "WORKTREE", work)
	git(t, dir, "commit", "-m", "stage second version")
	second := git(t, dir, "rev-parse", "HEAD")
	diff, err = repo.CommitFileDiff(ctx, second, name)
	check(diff, err, name, first, head, name, second, index)

	git(t, dir, "mv", "--", name, renamed)
	git(t, dir, "commit", "-m", "rename image")
	third := git(t, dir, "rev-parse", "HEAD")
	diff, err = repo.CompareFileDiff(ctx, second, third, renamed)
	check(diff, err, name, second, index, renamed, third, index)
	if err := os.Remove(filepath.Join(dir, renamed)); err != nil {
		t.Fatal(err)
	}
	diff, err = repo.Diff(ctx, renamed, false)
	check(diff, err, renamed, "INDEX", index, renamed, "WORKTREE", "")
}

func TestImageDiffLFSPointerPreservesTextAndPatch(t *testing.T) {
	repo, dir := fixture(t)
	ctx := t.Context()
	const path = "logo.png"
	pointer := func(digit string) string {
		return "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat(digit, 64) + "\nsize 123\n"
	}
	head, index, work := pointer("a"), pointer("b"), pointer("c")
	write(t, dir, path, head)
	git(t, dir, "add", "--", path)
	git(t, dir, "commit", "-m", "initial LFS pointer")
	write(t, dir, path, index)
	git(t, dir, "add", "--", path)
	write(t, dir, path, work)

	assertText := func(side *FileContent, want string) {
		t.Helper()
		if side == nil || !side.Present || side.Binary || side.Truncated || side.Unsupported ||
			side.ImageFormat != "" || side.ImageData != "" || side.Text != want {
			t.Fatalf("pointer side = %+v, want text %q", side, want)
		}
	}
	for _, tc := range []struct {
		staged     bool
		old, newer string
	}{
		{true, head, index},
		{false, index, work},
	} {
		diff, err := repo.Diff(ctx, path, tc.staged)
		if err != nil {
			t.Fatal(err)
		}
		assertText(diff.Old, tc.old)
		assertText(diff.New, tc.newer)
		patch, err := repo.Patch(ctx, path, tc.staged)
		if err != nil || !patch.Supported || len(patch.Hunks) != 1 ||
			!strings.Contains(patch.Text, "-oid sha256:") || !strings.Contains(patch.Text, "+oid sha256:") {
			t.Fatalf("staged=%v: pointer patch = %+v, err = %v", tc.staged, patch, err)
		}
	}

	before, err := repo.BrowseFileContent(ctx, path, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	assertText(before, head)

	malformed := strings.Replace(work, "size 123", "size -1", 1)
	write(t, dir, path, malformed)
	diff, err := repo.Diff(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	assertText(diff.Old, index)
	if side := diff.New; side == nil || side.Text != "" || !side.Binary || side.ImageFormat != "png" || side.ImageData != malformed {
		t.Fatalf("malformed pointer changed bitmap handling: %+v", side)
	}
}

func TestImageDiffReadLimitsAndUnsupportedSide(t *testing.T) {
	repo, dir := fixture(t)
	ctx := t.Context()
	for _, tc := range []struct {
		path string
		size int
		cut  bool
	}{
		{"bitmap.webp", fileContentLimit + 1, false},
		{"large.bmp", browserImageLimit + 1, true},
		{"large.SVG", fileContentLimit + 1, true},
	} {
		data := strings.Repeat("a", tc.size)
		write(t, dir, tc.path, data)
		content, err := repo.readWorktreeFileContent(ctx, newImageAwareFileContent(tc.path, "WORKTREE"))
		if err != nil {
			t.Fatal(err)
		}
		if !content.Present || content.Truncated != tc.cut || len(content.Text) != 0 ||
			tc.cut && content.ImageData != "" || !tc.cut && content.ImageData != data {
			t.Errorf("%s: truncated=%v, image bytes=%d", tc.path, content.Truncated, len(content.ImageData))
		}
	}
	write(t, dir, "vector.svg", "<svg/>")
	side, err := repo.readWorktreeFileContent(ctx, newImageAwareFileContent("vector.svg", "WORKTREE"))
	if err != nil || side.Text != "<svg/>" || side.ImageData != "" {
		t.Fatalf("SVG source = %+v, %v", side, err)
	}
	testSymlink(t, "vector.svg", filepath.Join(dir, "link.png"))
	side, err = repo.readWorktreeFileContent(ctx, newImageAwareFileContent("link.png", "WORKTREE"))
	if err != nil || !side.Unsupported || side.ImageData != "" {
		t.Fatalf("symlink = %+v, %v", side, err)
	}
}
