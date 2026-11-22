// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBrowseFilesIncludesTrackedNewAndDeletedButNotIgnored(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, ".gitignore", "ignored/\n*.log\n")
	write(t, dir, "src/tracked.go", "package main\n")
	write(t, dir, "deleted.txt", "deleted\n")
	write(t, dir, "staged-deleted.txt", "deleted\n")
	git(t, dir, "add", ".")
	write(t, dir, "ignored/tracked", "tracked despite ignore\n")
	git(t, dir, "add", "-f", "ignored/tracked")
	git(t, dir, "commit", "-m", "base")
	write(t, dir, "src/new.go", "package new\n")
	write(t, dir, "ignored/secret", "hidden\n")
	write(t, dir, "error.log", "hidden\n")
	if err := os.Remove(filepath.Join(dir, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "rm", "staged-deleted.txt")
	files, err := repo.BrowseFiles(ctx, "WORKTREE")
	want := []string{".gitignore", "deleted.txt", "ignored/tracked", "src/new.go", "src/tracked.go", "staged-deleted.txt"}
	if err != nil || !slices.Equal(files, want) {
		t.Fatalf("worktree files = %q, %v", files, err)
	}
	files, err = repo.BrowseFiles(ctx, "HEAD")
	if err != nil || slices.Contains(files, "src/new.go") || len(files) != 5 {
		t.Fatalf("HEAD files = %q, %v", files, err)
	}
	for _, path := range []string{"deleted.txt", "staged-deleted.txt"} {
		content, err := repo.BrowseFileContent(ctx, path, "WORKTREE")
		if err != nil || content.Present {
			t.Fatalf("deleted worktree = %+v, %v", content, err)
		}
		content, err = repo.BrowseFileContent(ctx, path, "HEAD")
		if err != nil || content.Text != "deleted\n" {
			t.Fatalf("deleted HEAD = %+v, %v", content, err)
		}
	}
	if _, err := repo.BrowseFileContent(ctx, "ignored/secret", "WORKTREE"); err == nil {
		t.Fatal("ignored content was exposed")
	}
}

func TestBlameLineMatchesPreviewVersions(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	const path = "notes [draft].txt"
	write(t, dir, path, "old\nstable\n")
	git(t, dir, "add", "--", path)
	git(t, dir, "commit", "-m", "first version")
	first := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, path, "head\nstable\n")
	git(t, dir, "commit", "-am", "change first line")
	head := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, path, "staged\nstable\n")
	git(t, dir, "add", "--", path)
	write(t, dir, path, "head\nworktree")
	write(t, dir, "new.txt", "new\n")
	for _, test := range []struct {
		revision string
		path     string
		line     int
		hash     string
		subject  string
	}{
		{first, path, 1, first, "first version"},
		{"HEAD", path, 1, head, "change first line"},
		{"INDEX", path, 1, "", ""},
		{"INDEX", path, 2, first, "first version"},
		{"WORKTREE", path, 1, head, "change first line"},
		{"WORKTREE", path, 2, "", ""},
		{"WORKTREE", "new.txt", 1, "", ""},
	} {
		content, err := repo.BrowseFileContent(ctx, test.path, test.revision)
		if err != nil {
			t.Fatal(err)
		}
		blame, err := repo.BlameLine(ctx, content, test.line)
		if err != nil || blame.Hash != test.hash || blame.Subject != test.subject ||
			test.hash != "" && (blame.Author == "" || blame.AuthoredAt.IsZero()) {
			t.Fatalf("%s %s:%d attribution = %+v, %v", test.revision, test.path, test.line, blame, err)
		}
	}
	worktree, err := repo.BrowseFileContent(ctx, path, "WORKTREE")
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, path, "after the preview\n")
	if blame, err := repo.BlameLine(ctx, worktree, 1); err != nil || blame.Hash != head {
		t.Fatalf("changed working file affected preview attribution: %+v, %v", blame, err)
	}
	git(t, dir, "commit", "--allow-empty", "-m", "move HEAD")
	if _, err := repo.BlameLine(ctx, worktree, 1); err == nil {
		t.Fatal("stale preview silently used the new HEAD")
	}
}

func TestBrowseContentLiteralPathsVersionsAndReadOnly(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	path := portableTestPath("目录/literal[?]\t.txt", "目录/literal[括号].txt")
	write(t, dir, path, "head\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	hash := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, path, "index\n")
	git(t, dir, "add", ".")
	write(t, dir, path, "worktree\n")
	before := git(t, dir, "status", "--porcelain=v1")
	for revision, want := range map[string]string{"HEAD": "head\n", hash: "head\n", "INDEX": "index\n", "WORKTREE": "worktree\n"} {
		content, err := repo.BrowseFileContent(ctx, path, revision)
		if err != nil || content.Text != want || content.Revision != revision {
			t.Fatalf("%s content = %+v, %v", revision, content, err)
		}
	}
	if after := git(t, dir, "status", "--porcelain=v1"); after != before {
		t.Fatal("browsing changed the index or worktree")
	}
	for _, path := range []string{"../outside", ".git/config", "目录/../file", ":(glob)**", "目录", "missing"} {
		if _, err := repo.BrowseFileContent(ctx, path, "WORKTREE"); err == nil {
			t.Fatalf("accepted invalid or unknown path %q", path)
		}
	}
	if _, err := repo.BrowseFiles(ctx, "--output=evil"); err == nil {
		t.Fatal("accepted arbitrary revision")
	}
}

func TestBrowseMembershipTreatsPathspecSyntaxAsLiteral(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, ".gitignore", "*.secret\n")
	write(t, dir, "keep.txt", "member\n")
	write(t, dir, "hidden.secret", "not a member\n")
	write(t, dir, "literal[1].txt", "literal tracked\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	for _, path := range []string{"*.txt", ":(glob)*.txt", "literal?.txt", "literal[1].txt"} {
		content, err := repo.BrowseFileContent(ctx, path, "WORKTREE")
		if path == "literal[1].txt" {
			if err != nil || content.Text != "literal tracked\n" {
				t.Fatalf("literal member = %+v, %v", content, err)
			}
		} else if err == nil {
			t.Fatalf("nonmember pathspec was interpreted: %q, %+v", path, content)
		}
	}
	for _, path := range []string{"*.txt", ":(glob)*.txt", "literal?.txt", "hidden.secret"} {
		content, err := repo.BrowseFileContent(ctx, path, "HEAD")
		if err != nil || content.Present || content.Text != "" {
			t.Fatalf("HEAD expanded nonliteral or ignored member %q: %+v, %v", path, content, err)
		}
	}
}

func TestBrowseContentGuardsAndCancellation(t *testing.T) {
	repo, dir := fixture(t)
	write(t, dir, "binary", "value\x00hidden")
	write(t, dir, "large", strings.Repeat("x", fileContentLimit+1))
	write(t, dir, "boundary", strings.Repeat("x", fileContentLimit))
	write(t, dir, "empty", "")
	ctx := context.Background()
	for _, path := range []string{"binary", "large", "boundary", "empty"} {
		content, err := repo.BrowseFileContent(ctx, path, "WORKTREE")
		if err != nil || !content.Present {
			t.Fatalf("%s = %+v, %v", path, content, err)
		}
		if path == "binary" && !content.Binary || path == "large" && !content.Truncated ||
			path == "boundary" && (content.Truncated || len(content.Text) != fileContentLimit) ||
			path == "empty" && (content.Binary || content.Truncated || content.Text != "") {
			t.Fatalf("unsafe content flags: %+v", content)
		}
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	for _, path := range []string{"binary", "large"} {
		content, err := repo.BrowseFileContent(ctx, path, "HEAD")
		if err != nil || path == "binary" && !content.Binary || path == "large" && !content.Truncated {
			t.Fatalf("HEAD %s = %+v, %v", path, content, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.BrowseFiles(canceled, "WORKTREE"); !errors.Is(err, context.Canceled) {
		t.Fatalf("list did not cancel: %v", err)
	}
	if _, err := repo.BrowseFileContent(canceled, "empty", "WORKTREE"); !errors.Is(err, context.Canceled) {
		t.Fatalf("content did not cancel: %v", err)
	}
}

func TestBrowseDoesNotFollowLinksOrReadSubmoduleContents(t *testing.T) {
	repo, dir := fixture(t)
	outside := t.TempDir()
	write(t, outside, "secret", "outside secret\n")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	write(t, dir, "nested/file", "inside\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "links")
	hash := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+hash+",module")
	if err := os.Mkdir(filepath.Join(dir, "module"), 0755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "module/private.txt", "submodule private\n")
	git(t, dir, "commit", "-m", "submodule")
	for _, revision := range []string{"WORKTREE", "HEAD", "INDEX"} {
		content, err := repo.BrowseFileContent(context.Background(), "link", revision)
		if err != nil || !content.Unsupported || content.Text != "" {
			t.Fatalf("link %s = %+v, %v", revision, content, err)
		}
		content, err = repo.BrowseFileContent(context.Background(), "module", revision)
		if err != nil || !content.Unsupported || content.Text != "" {
			t.Fatalf("submodule %s = %+v, %v", revision, content, err)
		}
		files, err := repo.BrowseFiles(context.Background(), revision)
		if err != nil || !slices.Contains(files, "module") || slices.Contains(files, "module/private.txt") {
			t.Fatalf("submodule contents were enumerated %s: %q, %v", revision, files, err)
		}
	}
	content, err := repo.BrowseFileContent(context.Background(), "module", "WORKTREE")
	if err != nil || !content.Unsupported {
		t.Fatalf("submodule = %+v, %v", content, err)
	}
	if err := os.Rename(filepath.Join(dir, "nested"), filepath.Join(dir, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "nested")); err != nil {
		t.Fatal(err)
	}
	content, err = repo.BrowseFileContent(context.Background(), "nested/file", "WORKTREE")
	if err != nil || !content.Unsupported || content.Text != "" {
		t.Fatalf("linked ancestor = %+v, %v", content, err)
	}
}

func TestBrowseUnbornRepository(t *testing.T) {
	repo, dir := fixture(t)
	write(t, dir, "new.txt", "new\n")
	files, err := repo.BrowseFiles(context.Background(), "WORKTREE")
	if err != nil || !slices.Equal(files, []string{"new.txt"}) {
		t.Fatalf("unborn files = %q, %v", files, err)
	}
	files, err = repo.BrowseFiles(context.Background(), "HEAD")
	if err != nil || len(files) != 0 {
		t.Fatalf("unborn HEAD files = %q, %v", files, err)
	}
}
