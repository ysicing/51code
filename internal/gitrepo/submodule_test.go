// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSubmoduleLifecycle(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	ctx := t.Context()
	parent, parentDir := fixture(t)
	_, source := fixture(t)
	write(t, source, "file", "first\n")
	git(t, source, "add", "file")
	git(t, source, "commit", "-m", "first")
	first := git(t, source, "rev-parse", "HEAD")
	const path = "deps/中文 组件"
	if err := parent.AddSubmodule(ctx, source, path); err != nil {
		t.Fatal(err)
	}
	git(t, parentDir, "commit", "-m", "add module")
	_, checkout := fixture(t)
	git(t, checkout, "fetch", parentDir, "main")
	git(t, checkout, "checkout", "FETCH_HEAD")
	repo, err := Open(ctx, checkout)
	if err != nil {
		t.Fatal(err)
	}
	read := func(head, target string) Submodule {
		t.Helper()
		items, err := repo.Submodules(ctx)
		if err != nil || len(items) != 1 || items[0].Path != path || items[0].Head != head || items[0].Target != target {
			t.Fatalf("submodules = %+v, %v", items, err)
		}
		return items[0]
	}
	item := read("", first)
	if _, err := repo.SubmodulePath(ctx, path); err == nil {
		t.Fatal("uninitialized module can be opened")
	}
	write(t, checkout, path+"/untracked", "keep me\n")
	if err := repo.UpdateSubmodule(ctx, item); err == nil {
		t.Fatal("initialization accepted a directory with unrelated files")
	}
	if err := os.Remove(filepath.Join(checkout, path, "untracked")); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateSubmodule(ctx, item); err != nil {
		t.Fatal(err)
	}
	read(first, first)
	module, err := repo.SubmodulePath(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, source, "ignored.txt", "from source\n")
	git(t, source, "add", "ignored.txt")
	git(t, source, "commit", "-m", "second")
	second := git(t, source, "rev-parse", "HEAD")
	git(t, module, "fetch", "origin")
	git(t, checkout, "update-index", "--cacheinfo", "160000,"+second+","+path)
	index := git(t, checkout, "ls-files", "--stage")
	if err := repo.UpdateSubmodule(ctx, item); err == nil {
		t.Fatal("stale review accepted after the recorded commit changed")
	}
	item = read(first, second)
	write(t, module, "file", "local draft\n")
	if !read(first, second).Dirty || repo.UpdateSubmodule(ctx, item) == nil {
		t.Fatal("dirty module was not protected")
	}
	content, err := os.ReadFile(filepath.Join(module, "file"))
	if err != nil || string(content) != "local draft\n" {
		t.Fatalf("local draft = %q, %v", content, err)
	}
	git(t, module, "restore", "file")
	excludes := filepath.Join(t.TempDir(), "excludes")
	if err := os.WriteFile(excludes, []byte("ignored.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, module, "config", "core.excludesFile", excludes)
	write(t, module, "ignored.txt", "ignored draft\n")
	if err := repo.UpdateSubmodule(ctx, item); err == nil {
		t.Fatal("update overwrote colliding ignored content")
	}
	content, err = os.ReadFile(filepath.Join(module, "ignored.txt"))
	if err != nil || string(content) != "ignored draft\n" {
		t.Fatalf("ignored draft = %q, %v", content, err)
	}
	if err := os.Remove(filepath.Join(module, "ignored.txt")); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateSubmodule(ctx, item); err != nil {
		t.Fatal(err)
	}
	read(second, second)
	if git(t, checkout, "ls-files", "--stage") != index {
		t.Fatal("updating a submodule modified the parent index")
	}
	git(t, module, "-c", "user.name=Test User", "-c", "user.email=test@example.test",
		"-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "local detached commit")
	local := git(t, module, "rev-parse", "HEAD")
	item = read(local, second)
	if !item.Detached || repo.UpdateSubmodule(ctx, item) == nil {
		t.Fatal("updating would abandon a local detached commit")
	}
	git(t, module, "branch", "saved", local)
	if err := repo.UpdateSubmodule(ctx, item); err != nil {
		t.Fatal(err)
	}
	read(second, second)
	if git(t, module, "rev-parse", "saved") != local {
		t.Fatal("update changed the saved local branch")
	}
}

func TestSubmoduleStatusWithUpstreamDivergence(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	ctx := t.Context()
	parent, parentDir := fixture(t)
	_, source := fixture(t)
	git(t, source, "commit", "--allow-empty", "-m", "base")
	target := git(t, source, "rev-parse", "HEAD")
	const path = "deps/module"
	if err := parent.AddSubmodule(ctx, source, path); err != nil {
		t.Fatal(err)
	}
	moduleDir := filepath.Join(parentDir, path)
	git(t, moduleDir, "switch", "main")
	git(t, moduleDir, "branch", "--set-upstream-to=origin/main", "main")
	repo, err := Open(ctx, parentDir)
	if err != nil {
		t.Fatal(err)
	}
	read := func(wantHead string) {
		t.Helper()
		items, err := repo.Submodules(ctx)
		if err != nil || len(items) != 1 {
			t.Fatalf("submodules = %+v, %v", items, err)
		}
		item := items[0]
		if item.Head != wantHead || item.Target != target || item.Problem != "" || item.Detached || item.Dirty {
			t.Fatalf("upstream divergence marked the submodule unavailable: %+v, %v", item, err)
		}
	}

	git(t, moduleDir, "commit", "--allow-empty", "-m", "ahead")
	ahead := git(t, moduleDir, "rev-parse", "HEAD")
	read(ahead)
	git(t, moduleDir, "reset", "--hard", "origin/main")

	git(t, source, "commit", "--allow-empty", "-m", "behind")
	git(t, moduleDir, "fetch", "origin")
	read(target)
}

func TestAddSubmodulePreservesUnstagedGitmodules(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	repo, dir := fixture(t)
	_, source := fixture(t)
	git(t, source, "commit", "--allow-empty", "-m", "source")
	git(t, dir, "commit", "--allow-empty", "-m", "base")
	const draft = "# unstaged draft\n"
	write(t, dir, ".gitmodules", draft)
	if err := repo.AddSubmodule(t.Context(), source, "deps/module"); err == nil {
		t.Fatal("adding a submodule would stage an untracked .gitmodules draft")
	}
	content, err := os.ReadFile(filepath.Join(dir, ".gitmodules"))
	if err != nil || string(content) != draft || git(t, dir, "ls-files", "--stage") != "" {
		t.Fatalf(".gitmodules or index changed: %q, %v", content, err)
	}
}
