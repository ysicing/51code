// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWorktreeSearchAndBrowseBeyondOutputLimit(t *testing.T) {
	ctx := context.Background()
	repo, dir := fixture(t)
	write(t, dir, "match.txt", "needle\n")
	git(t, dir, "add", "match.txt")
	blob := git(t, dir, "rev-parse", ":match.txt")
	prefix := strings.Repeat(strings.Repeat("a", 180)+"/", 4)
	count := outputLimit/len(prefix) + 1
	var entries strings.Builder
	for index := range count {
		fmt.Fprintf(&entries, "100644 %s\t%s%05d.txt\n", blob, prefix, index)
	}
	if _, err := run(ctx, dir, entries.String(), 30*time.Second, "update-index", "--index-info"); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-qm", "large index")
	var matches []WorktreeSearchMatch
	limited, err := repo.SearchWorktree(ctx, "needle", func(batch []WorktreeSearchMatch) { matches = append(matches, batch...) })
	if err != nil || limited || len(matches) != 1 || matches[0].Path != "match.txt" {
		t.Fatalf("search: %+v, limited %t, err %v", matches, limited, err)
	}
	for _, revision := range []string{"WORKTREE", "HEAD", "INDEX"} {
		paths, err := repo.BrowseFiles(ctx, revision)
		if err != nil || len(paths) != count+1 || !slices.Contains(paths, matches[0].Path) {
			t.Fatalf("browse %s: %d paths, err %v", revision, len(paths), err)
		}
	}
	content, err := repo.BrowseFileContent(ctx, matches[0].Path, "WORKTREE")
	if err != nil || content.Text != "needle\n" {
		t.Fatalf("preview: %+v, err %v", content, err)
	}
}

func TestWorktreeSearchIncludesLocalTextAndKeepsBackendParity(t *testing.T) {
	repo, dir := fixture(t)
	write(t, dir, ".gitignore", "ignored/\n")
	write(t, dir, "tracked.txt", "old version\n")
	write(t, dir, "deleted.txt", "-x.y\n")
	write(t, dir, "ignored/tracked.txt", "-x.y\n")
	git(t, dir, "add", ".gitignore", "tracked.txt", "deleted.txt")
	git(t, dir, "add", "-f", "ignored/tracked.txt")
	git(t, dir, "commit", "-m", "base")
	if err := os.Remove(filepath.Join(dir, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "tracked.txt", "no match\r\n中文 -X.Y and -x.y\r\n")
	write(t, dir, "literal[1].txt", "last line -x.y")
	write(t, dir, "ignored/new.txt", "-x.y\n")
	write(t, dir, "binary", "\x00-x.y")
	write(t, dir, "large", strings.Repeat("a", fileContentLimit)+"-x.y")
	write(t, dir, "unrelated.txt", "-xay")
	external := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(external, []byte("-x.y\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(external, filepath.Join(dir, "linked.txt"))

	want := []WorktreeSearchMatch{
		{Path: "ignored/tracked.txt", Line: 1, Text: "-x.y"},
		{Path: "literal[1].txt", Line: 1, Column: 10, Text: "last line -x.y"},
		{Path: "tracked.txt", Line: 2, Column: 7, Text: "中文 -X.Y and -x.y"},
	}
	for name, executable := range map[string]string{"Go": "", "fallback": filepath.Join(dir, "missing-rg"), "ripgrep": findRipgrep()} {
		t.Run(name, func(t *testing.T) {
			if name == "ripgrep" && executable == "" {
				t.Skip("rg unavailable")
			}
			var got []WorktreeSearchMatch
			limited, err := repo.searchWorktree(context.Background(), "-x.y", executable, func(batch []WorktreeSearchMatch) {
				got = append(got, batch...)
			})
			if err != nil || limited || !slices.Equal(got, want) {
				t.Fatalf("matches = %+v, limited = %t, err = %v", got, limited, err)
			}
		})
	}
}

func TestWorktreeRipgrepBatchMapsMatchedLines(t *testing.T) {
	executable := findRipgrep()
	if executable == "" {
		t.Skip("rg unavailable")
	}
	files := []worktreeSearchFile{{"a", "a: no match"}, {"b", "first\n命中 KEY.[1]\r\n"}, {"c", "key.[1]\n"}}
	pattern := regexp.MustCompile(worktreeLiteralPattern("key.[1]"))
	want := scanWorktreeBatch(context.Background(), pattern, files, 1)
	got, ok := grepWorktreeBatch(context.Background(), executable, pattern, files, 1)
	if !ok || len(want) != 1 || !slices.Equal(got, want) {
		t.Fatalf("ripgrep = %+v, ok = %t, want %+v", got, ok, want)
	}
}

func TestWorktreeSearchLimitAndCancellation(t *testing.T) {
	repo, dir := fixture(t)
	write(t, dir, "first.txt", strings.Repeat("key\n", WorktreeSearchLimit))
	write(t, dir, "second.txt", "key")
	ctx := context.Background()
	count := 0
	limited, err := repo.searchWorktree(ctx, "key", "", func(batch []WorktreeSearchMatch) { count += len(batch) })
	if err != nil || !limited || count != WorktreeSearchLimit {
		t.Fatalf("limit = %t, count = %d, err = %v", limited, count, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	_, err = repo.searchWorktree(canceled, "key", "", func([]WorktreeSearchMatch) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled search = %v", err)
	}
}
