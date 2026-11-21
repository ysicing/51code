// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"slices"
	"testing"
)

func TestBranchesIdentifyDefault(t *testing.T) {
	for _, test := range []struct {
		name     string
		main     string
		heads    map[string]string
		expected []string
	}{
		{"main-fallback", "main", nil, []string{"main"}},
		{"master-fallback", "master", nil, []string{"master"}},
		{"custom-without-metadata", "trunk", nil, nil},
		{"origin-head", "main", map[string]string{"origin": "release/stable", "backup": "main"}, []string{"release/stable", "origin/release/stable", "backup/main"}},
		{"custom-remote-name", "main", map[string]string{"team/mirror": "release/stable"}, []string{"release/stable", "team/mirror/release/stable"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, path := fixture(t)
			git(t, path, "commit", "--allow-empty", "-m", "base")
			if test.main != "main" {
				git(t, path, "branch", "-m", test.main)
			}
			git(t, path, "branch", "release/stable")
			git(t, path, "switch", "-c", "a-topic")
			for remote, branch := range test.heads {
				git(t, path, "update-ref", "refs/remotes/"+remote+"/"+branch, "HEAD")
				git(t, path, "symbolic-ref", "refs/remotes/"+remote+"/HEAD", "refs/remotes/"+remote+"/"+branch)
			}
			branches, err := repo.Branches(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var defaults []string
			for _, branch := range branches {
				if branch.Default {
					defaults = append(defaults, branch.Name)
				}
				if branch.Current != (branch.Name == "a-topic") {
					t.Fatalf("default detection changed current branch: %+v", branch)
				}
			}
			slices.Sort(defaults)
			slices.Sort(test.expected)
			if !slices.Equal(defaults, test.expected) {
				t.Fatalf("defaults=%v expected=%v", defaults, test.expected)
			}
		})
	}
}

func TestBranchesReadConfiguredUpstreamTarget(t *testing.T) {
	repo, path := fixture(t)
	git(t, path, "commit", "--allow-empty", "-m", "base")
	git(t, path, "remote", "add", "team/mirror", t.TempDir())
	git(t, path, "config", "remote.team/mirror.fetch", "+refs/heads/*:refs/remotes/cache/*")
	git(t, path, "update-ref", "refs/remotes/cache/release", "HEAD")
	git(t, path, "config", "branch.main.remote", "team/mirror")
	git(t, path, "config", "branch.main.merge", "refs/heads/release")
	branches, err := repo.Branches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, branch := range branches {
		if branch.Current {
			if branch.UpstreamRemote != "team/mirror" || branch.UpstreamBranch != "release" {
				t.Fatalf("lost configured upstream target: %+v", branch)
			}
			return
		}
	}
	t.Fatal("missing current branch")
}

func TestBranchesReportDivergenceFromOwnUpstream(t *testing.T) {
	repo, path := fixture(t)
	git(t, path, "commit", "--allow-empty", "-m", "base")
	base := git(t, path, "rev-parse", "HEAD")
	git(t, path, "remote", "add", "origin", t.TempDir())
	git(t, path, "update-ref", "refs/remotes/origin/main", base)
	git(t, path, "branch", "--set-upstream-to=origin/main", "main")
	git(t, path, "branch", "synced", base)
	git(t, path, "branch", "--set-upstream-to=origin/main", "synced")
	git(t, path, "branch", "untracked", base)
	git(t, path, "update-ref", "refs/remotes/origin/gone", base)
	git(t, path, "branch", "gone", base)
	git(t, path, "branch", "--set-upstream-to=origin/gone", "gone")
	git(t, path, "update-ref", "-d", "refs/remotes/origin/gone")
	git(t, path, "switch", "--detach", base)
	for range 3 {
		git(t, path, "commit", "--allow-empty", "-m", "upstream")
	}
	git(t, path, "update-ref", "refs/remotes/origin/topic", "HEAD")
	git(t, path, "branch", "behind", base)
	git(t, path, "branch", "--set-upstream-to=origin/topic", "behind")
	git(t, path, "switch", "-c", "topic", base)
	for range 2 {
		git(t, path, "commit", "--allow-empty", "-m", "topic")
	}
	git(t, path, "branch", "--set-upstream-to=origin/topic", "topic")
	git(t, path, "switch", "main")
	git(t, path, "commit", "--allow-empty", "-m", "main")
	git(t, path, "tag", "base", base)

	ctx := context.Background()
	branches, err := repo.Branches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := repo.References(ctx)
	if err != nil || !slices.Equal(branches, refs.Branches) {
		t.Fatalf("references differ from branches: %+v, %v", refs, err)
	}
	if len(refs.Tags) != 1 || refs.Tags[0].Hash != base {
		t.Fatalf("reference query lost the full tag hash: %+v", refs.Tags)
	}
	want := map[string][2]int{
		"main": {1, 0}, "topic": {2, 3}, "behind": {0, 3},
		"synced": {}, "untracked": {}, "gone": {},
		"origin/main": {}, "origin/topic": {},
	}
	if len(branches) != len(want) {
		t.Fatalf("unexpected branches: %+v", branches)
	}
	for _, branch := range branches {
		expected, ok := want[branch.Name]
		if !ok || [2]int{branch.Ahead, branch.Behind} != expected {
			t.Errorf("%s ahead/behind = %d/%d, want %v", branch.Name, branch.Ahead, branch.Behind, expected)
		}
	}
}
