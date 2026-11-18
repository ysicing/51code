// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
)

// stubSnapshots 替换仓库快照：fast 立即返回，slow 等 fast 完成后取消扫描并阻塞到 ctx 结束，
// 使“部分完成后被取消”的时序确定。
func stubSnapshots(t *testing.T, fast, slow string, cancel context.CancelFunc) {
	t.Helper()
	previous := snapshotRepository
	t.Cleanup(func() { snapshotRepository = previous })
	fastDone := make(chan struct{})
	snapshotRepository = func(ctx context.Context, path string) (gitrepo.Snapshot, error) {
		switch path {
		case fast:
			defer close(fastDone)
			return gitrepo.Snapshot{Name: filepath.Base(path), Path: path, Branch: "fresh"}, nil
		case slow:
			<-fastDone
			cancel()
			<-ctx.Done()
			return gitrepo.Snapshot{}, ctx.Err()
		}
		return gitrepo.Snapshot{}, errors.New("unexpected path " + path)
	}
}

func TestScanSkipsRepositoryContents(t *testing.T) {
	root := t.TempDir()
	outer := localRepo(t, filepath.Join(root, "outer"))
	// 仓库内部的嵌套仓库（含子模块）按设计不再被单独发现。
	localRepo(t, filepath.Join(outer, "build", "inner"))
	sibling := localRepo(t, filepath.Join(root, "sibling"))
	manager, _ := New(filepath.Join(t.TempDir(), "recent.json"))
	result, err := manager.Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 2 || result.Repositories[0].Path != outer || result.Repositories[1].Path != sibling || result.Truncated {
		t.Fatalf("scan entered repository contents: %+v", result)
	}
}

func TestScanFindsRepositoriesUnderInvalidGitMarkers(t *testing.T) {
	root := t.TempDir()
	invalidDirectory := filepath.Join(root, "invalid-directory")
	invalidFile := filepath.Join(root, "invalid-file")
	first := localRepo(t, filepath.Join(invalidDirectory, "nested"))
	second := localRepo(t, filepath.Join(invalidFile, "nested"))
	if err := os.Mkdir(filepath.Join(invalidDirectory, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalidFile, ".git"), []byte("gitdir: missing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(filepath.Join(t.TempDir(), "recent.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Scan(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 2 || result.Repositories[0].Path != first || result.Repositories[1].Path != second || result.Truncated || len(result.Warnings) != 2 {
		t.Fatalf("scan with invalid Git markers: %+v", result)
	}
}

func TestScanReturnsCompletedRepositoriesWhenCancelled(t *testing.T) {
	root := t.TempDir()
	fast := localRepo(t, filepath.Join(root, "a"))
	slow := localRepo(t, filepath.Join(root, "b"))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	fastDone, slowStarted := make(chan struct{}), make(chan struct{})
	snapshot := func(repo *gitrepo.Repository, ctx context.Context) (gitrepo.Snapshot, error) {
		switch repo.Path {
		case fast:
			select {
			case <-slowStarted:
			case <-ctx.Done():
				return gitrepo.Snapshot{}, ctx.Err()
			}
			defer close(fastDone)
			return gitrepo.Snapshot{Name: "a", Path: fast, Branch: "fresh"}, nil
		case slow:
			close(slowStarted)
			select {
			case <-fastDone:
				cancel()
			case <-ctx.Done():
			}
			return gitrepo.Snapshot{}, ctx.Err()
		}
		return gitrepo.Snapshot{}, errors.New("unexpected path " + repo.Path)
	}
	manager, _ := New(filepath.Join(t.TempDir(), "recent.json"))
	result, err := manager.scan(ctx, root, snapshot)
	if !errors.Is(err, ErrPartial) || !errors.Is(err, context.Canceled) {
		t.Fatalf("partial scan error: %v", err)
	}
	if len(result.Repositories) != 1 || result.Repositories[0].Path != fast || !result.Truncated || len(result.Warnings) != 0 {
		t.Fatalf("partial scan result: %+v", result)
	}
}

func TestLibraryRefreshKeepsCompletedEntriesWhenCancelled(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "recent.json")
	manager, _ := New(settings)
	root := t.TempDir()
	fast, slow := filepath.Join(root, "a"), filepath.Join(root, "b")
	if _, err := manager.update(func(next *Library) error {
		next.Repositories = []LocalRepository{
			{Name: "a", Path: fast, Branch: "old", GroupID: "g"},
			{Name: "b", Path: slow, Branch: "old"},
		}
		next.Groups = []Group{{ID: "g", Name: "组"}}
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stubSnapshots(t, fast, slow, cancel)
	library, err := manager.RefreshLibrary(ctx)
	if !errors.Is(err, ErrPartial) || !errors.Is(err, context.Canceled) {
		t.Fatalf("partial refresh error: %v", err)
	}
	check := func(library Library) {
		t.Helper()
		first, second := library.Repositories[0], library.Repositories[1]
		if first.Branch != "fresh" || first.GroupID != "g" || second.Branch != "old" || second.Problem != "" {
			t.Fatalf("partial refresh entries: %+v", library.Repositories)
		}
	}
	check(library)
	check(manager.Library())
	reloaded, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}
	check(reloaded.Library())
}

func TestLibraryPartialRefreshPreservesConcurrentRememberAndMove(t *testing.T) {
	for _, test := range []struct {
		name   string
		branch string
	}{
		{name: "changed summary", branch: "remembered"},
		{name: "unchanged summary", branch: "old"},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := filepath.Join(t.TempDir(), "recent.json")
			manager, err := New(settings)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			remembered := filepath.Join(root, "remembered")
			moved := filepath.Join(root, "moved")
			slow := filepath.Join(root, "slow")
			if _, err := manager.update(func(next *Library) error {
				next.Repositories = []LocalRepository{
					{Name: "remembered", Path: remembered, Branch: "old"},
					{Name: "moved", Path: moved, Branch: "old"},
					{Name: "slow", Path: slow, Branch: "old"},
				}
				next.Groups = []Group{{ID: "g", Name: "组"}}
				return nil
			}, nil); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			previous := snapshotRepository
			t.Cleanup(func() { snapshotRepository = previous })
			rememberedDone, movedDone := make(chan struct{}), make(chan struct{})
			snapshotRepository = func(ctx context.Context, path string) (gitrepo.Snapshot, error) {
				switch path {
				case remembered:
					defer close(rememberedDone)
					return gitrepo.Snapshot{Name: "remembered", Path: remembered, Branch: "stale"}, nil
				case moved:
					defer close(movedDone)
					return gitrepo.Snapshot{Name: "moved", Path: moved, Branch: "fresh"}, nil
				case slow:
					<-rememberedDone
					<-movedDone
					if err := manager.Remember(gitrepo.Snapshot{Name: "remembered", Path: remembered, Branch: test.branch}); err != nil {
						t.Errorf("remember: %v", err)
					}
					for _, path := range []string{remembered, moved} {
						if _, err := manager.Move(path, "g"); err != nil {
							t.Errorf("move %s: %v", path, err)
						}
					}
					cancel()
					return gitrepo.Snapshot{}, ctx.Err()
				}
				return gitrepo.Snapshot{}, errors.New("unexpected path " + path)
			}

			library, err := manager.RefreshLibrary(ctx)
			if !errors.Is(err, ErrPartial) || !errors.Is(err, context.Canceled) {
				t.Fatalf("partial refresh error: %v", err)
			}
			want := []LocalRepository{
				{Name: "remembered", Path: remembered, Branch: test.branch, GroupID: "g"},
				{Name: "moved", Path: moved, Branch: "fresh", GroupID: "g"},
				{Name: "slow", Path: slow, Branch: "old"},
			}
			check := func(library Library) {
				t.Helper()
				if !slices.Equal(library.Repositories, want) {
					t.Fatalf("partial refresh entries: got %+v, want %+v", library.Repositories, want)
				}
			}
			check(library)
			check(manager.Library())
			reloaded, err := New(settings)
			if err != nil {
				t.Fatal(err)
			}
			check(reloaded.Library())
		})
	}
}

func TestLibraryReadsDoNotWaitForDiskWrites(t *testing.T) {
	manager, _ := New(filepath.Join(t.TempDir(), "recent.json"))
	// 持有写锁模拟正在落盘的修改。
	manager.writeMu.Lock()
	read := make(chan Library, 1)
	go func() { read <- manager.Library() }()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		manager.writeMu.Unlock()
		t.Fatal("Library() waited for an in-progress write")
	}
	written := make(chan error, 1)
	go func() {
		_, err := manager.CreateGroup("排队")
		written <- err
	}()
	select {
	case err := <-written:
		manager.writeMu.Unlock()
		t.Fatalf("concurrent write was not serialized: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	manager.writeMu.Unlock()
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if groups := manager.Library().Groups; len(groups) != 1 || groups[0].Name != "排队" {
		t.Fatalf("queued write lost: %+v", groups)
	}
}
