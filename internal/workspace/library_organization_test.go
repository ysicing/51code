// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"
)

func TestRemoteLibraryPersistsHostAndPath(t *testing.T) {
	store := filepath.Join(t.TempDir(), "library.json")
	manager, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	first := gitrepo.SSHWorkspace{Host: "devbox", Path: "/srv/api"}
	second := gitrepo.SSHWorkspace{Host: "staging", Path: "/srv/api"}
	for _, repo := range []gitrepo.SSHWorkspace{first, second, first} {
		if _, err := manager.RememberSSH(t.Context(), repo); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	library := reloaded.Library()
	if len(library.Repositories) != 0 || !slices.Equal(library.SSHRepositories, []gitrepo.SSHWorkspace{second, first}) {
		t.Fatalf("远端记录未正确保存: %+v", library)
	}
	remaining, err := reloaded.ForgetSSH(first)
	if err != nil || !slices.Equal(remaining.SSHRepositories, []gitrepo.SSHWorkspace{second}) {
		t.Fatalf("移除影响其他主机的同名仓库: %+v, %v", remaining, err)
	}
}

func TestLibraryRepositoryOrderKeepsLegacyAndTiedEntriesStable(t *testing.T) {
	library := Library{Groups: []Group{}, Repositories: []LocalRepository{
		{Name: "legacy-z", Path: "/z"},
		{Name: "older", Path: "/older", LastOpenedAt: 100},
		{Name: "legacy-a", Path: "/a"},
		{Name: "newest", Path: "/newest", LastOpenedAt: 200},
		{Name: "tied", Path: "/tied", LastOpenedAt: 100},
	}}
	original := copyLibrary(library)
	for _, order := range []string{"", "unknown"} {
		if got := library.OrderedRepositories(order); !slices.Equal(got, original.Repositories) {
			t.Fatalf("default changed persisted ordering: %+v", got)
		}
	}
	recent := library.OrderedRepositories("recent")
	want := []string{"/newest", "/older", "/tied", "/z", "/a"}
	for index, entry := range recent {
		if entry.Path != want[index] {
			t.Fatalf("recent order: %+v", recent)
		}
	}
	recent[0].Name = "changed returned copy"
	if !reflect.DeepEqual(library, original) {
		t.Fatal("sorting mutated the library")
	}
}

func TestLibraryMarkOpenedPersistsWithoutMarkingRefreshRememberOrAdd(t *testing.T) {
	file := filepath.Join(t.TempDir(), "library.json")
	manager, err := New(file)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := t.TempDir()
	first := localRepo(t, filepath.Join(root, "first"))
	second := localRepo(t, filepath.Join(root, "second"))
	library, err := manager.Add(ctx, []string{first, second}, "")
	if err != nil {
		t.Fatal(err)
	}
	if library.Repositories[0].LastOpenedAt != 0 || library.Repositories[1].LastOpenedAt != 0 {
		t.Fatal("adding repositories marked them opened")
	}
	library, err = manager.MarkOpened(ctx, second)
	if err != nil || library.Repositories[1].LastOpenedAt == 0 {
		t.Fatalf("mark opened: %+v %v", library, err)
	}
	stamp := library.Repositories[1].LastOpenedAt
	if _, err := manager.Open(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remember(gitrepo.Snapshot{Name: "updated", Path: second, Branch: "topic"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RefreshLibrary(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Add(ctx, []string{second}, ""); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(file)
	if err != nil {
		t.Fatal(err)
	}
	library = reloaded.Library()
	if library.Repositories[0].Path != first || library.Repositories[0].LastOpenedAt != 0 ||
		library.Repositories[1].Path != second || library.Repositories[1].LastOpenedAt != stamp {
		t.Fatalf("metadata refresh changed recency or stored order: %+v", library)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := manager.MarkOpened(cancelled, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open marker: %v", err)
	}
	if _, err := manager.MarkOpened(ctx, "/unknown"); err == nil {
		t.Fatal("marked an absent entry opened")
	}
}

func TestLibraryRefreshKeepsConcurrentOpenTimestamp(t *testing.T) {
	manager, _ := New(filepath.Join(t.TempDir(), "library.json"))
	path := localRepo(t, filepath.Join(t.TempDir(), "project"))
	if _, err := manager.Add(context.Background(), []string{path}, ""); err != nil {
		t.Fatal(err)
	}
	original := snapshotRepository
	t.Cleanup(func() { snapshotRepository = original })
	started, release := make(chan struct{}), make(chan struct{})
	snapshotRepository = func(_ context.Context, current string) (gitrepo.Snapshot, error) {
		close(started)
		<-release
		return gitrepo.Snapshot{Name: "refreshed", Path: current}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := manager.RefreshLibrary(context.Background())
		done <- err
	}()
	<-started
	library, err := manager.MarkOpened(context.Background(), path)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	current := manager.Library().Repositories[0]
	if current.LastOpenedAt != library.Repositories[0].LastOpenedAt || current.Name != "refreshed" {
		t.Fatalf("refresh lost concurrent metadata: %+v", current)
	}
}

func relocationFixture(t *testing.T) (*Manager, LocalRepository, string) {
	t.Helper()
	manager, err := New(filepath.Join(t.TempDir(), "library.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	library, err := manager.CreateGroup("工作")
	if err != nil {
		t.Fatal(err)
	}
	path := localRepo(t, filepath.Join(t.TempDir(), "before"))
	if _, err := manager.Add(ctx, []string{path}, library.Groups[0].ID); err != nil {
		t.Fatal(err)
	}
	library, err = manager.MarkOpened(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(path), "after")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	return manager, library.Repositories[0], target
}

func TestLibraryRelocatePreservesGroupRecencyAndPositionWithoutOpening(t *testing.T) {
	manager, expected, target := relocationFixture(t)
	library, err := manager.Relocate(context.Background(), expected, target)
	if err != nil {
		t.Fatal(err)
	}
	entry := library.Repositories[0]
	if entry.Path != target || entry.Name != "after" || entry.GroupID != expected.GroupID ||
		entry.LastOpenedAt != expected.LastOpenedAt || entry.Problem != "" {
		t.Fatalf("relocation lost entry metadata: %+v", entry)
	}
	if _, err := manager.Get(target); err == nil {
		t.Fatal("relocation implicitly opened the replacement")
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Fatal("relocation modified the replacement on disk", err)
	}
	if _, err := os.Stat(expected.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("relocation recreated the old directory")
	}
	reloaded, err := New(manager.file)
	if err != nil || !reflect.DeepEqual(reloaded.Library(), library) {
		t.Fatalf("relocation did not persist: %v", err)
	}
}

func TestLibraryRelocateFailureKeepsEntry(t *testing.T) {
	for _, kind := range []string{"invalid", "cancelled", "duplicate", "duplicate-alias", "changed", "removed", "valid-original", "persist"} {
		t.Run(kind, func(t *testing.T) {
			manager, expected, target := relocationFixture(t)
			ctx := context.Background()
			switch kind {
			case "invalid":
				target = t.TempDir()
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "duplicate", "duplicate-alias":
				if _, err := manager.Add(ctx, []string{target}, ""); err != nil {
					t.Fatal(err)
				}
				if kind == "duplicate-alias" {
					alias := filepath.Join(t.TempDir(), "alias")
					if err := os.Symlink(target, alias); err != nil {
						t.Skip("symlinks unavailable", err)
					}
					target = alias
				}
			case "changed":
				if _, err := manager.Move(expected.Path, ""); err != nil {
					t.Fatal(err)
				}
			case "removed":
				if _, err := manager.Remove(expected.Path); err != nil {
					t.Fatal(err)
				}
			case "valid-original":
				localRepo(t, expected.Path)
			case "persist":
				manager.file = t.TempDir()
			}
			before := manager.Library()
			if _, err := manager.Relocate(ctx, expected, target); err == nil {
				t.Fatal("unsafe relocation succeeded")
			}
			if !reflect.DeepEqual(before, manager.Library()) {
				t.Fatal("failed relocation changed the library")
			}
		})
	}
}

func TestLibraryRelocateRejectsRecordChangedDuringValidation(t *testing.T) {
	manager, expected, target := relocationFixture(t)
	original := snapshotRepository
	t.Cleanup(func() { snapshotRepository = original })
	snapshotRepository = func(ctx context.Context, path string) (gitrepo.Snapshot, error) {
		if _, err := manager.Move(expected.Path, ""); err != nil {
			return gitrepo.Snapshot{}, err
		}
		return original(ctx, path)
	}
	if _, err := manager.Relocate(context.Background(), expected, target); err == nil {
		t.Fatal("validation overwrote a concurrently regrouped entry")
	}
	current := manager.Library().Repositories[0]
	if current.Path != expected.Path || current.GroupID != "" || current.LastOpenedAt != expected.LastOpenedAt {
		t.Fatalf("stale relocation changed the current entry: %+v", current)
	}
}

func TestLibraryRelocateCancellationDuringValidationKeepsEntry(t *testing.T) {
	manager, expected, target := relocationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := snapshotRepository
	t.Cleanup(func() { snapshotRepository = original })
	snapshotRepository = func(ctx context.Context, path string) (gitrepo.Snapshot, error) {
		snapshot, err := original(ctx, path)
		cancel()
		return snapshot, err
	}
	if _, err := manager.Relocate(ctx, expected, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled validation: %v", err)
	}
	if current := manager.Library().Repositories[0]; current != expected {
		t.Fatalf("cancelled relocation changed the entry: %+v", current)
	}
}

func TestLibraryMarkOpenedPersistenceFailureKeepsTimestamp(t *testing.T) {
	manager, expected, _ := relocationFixture(t)
	manager.file = t.TempDir()
	if _, err := manager.MarkOpened(context.Background(), expected.Path); err == nil {
		t.Fatal("open marker ignored persistence failure")
	}
	if current := manager.Library().Repositories[0]; current != expected {
		t.Fatal("failed open marker changed the in-memory entry")
	}
}

func TestLibraryRelocateRejectsFilesystemChangeDuringValidation(t *testing.T) {
	for _, kind := range []string{"source-reappeared", "target-replaced"} {
		t.Run(kind, func(t *testing.T) {
			manager, expected, target := relocationFixture(t)
			other := localRepo(t, filepath.Join(t.TempDir(), "other"))
			original := snapshotRepository
			t.Cleanup(func() { snapshotRepository = original })
			snapshotRepository = func(ctx context.Context, path string) (gitrepo.Snapshot, error) {
				switch kind {
				case "source-reappeared":
					localRepo(t, expected.Path)
				case "target-replaced":
					if err := os.Rename(target, filepath.Join(t.TempDir(), "moved")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(other, target); err != nil {
						t.Skip("symlinks unavailable", err)
					}
				}
				return original(ctx, path)
			}
			if _, err := manager.Relocate(context.Background(), expected, target); err == nil {
				t.Fatal("filesystem change during validation was accepted")
			}
			if current := manager.Library().Repositories[0]; current != expected {
				t.Fatalf("changed filesystem replaced the library entry: %+v", current)
			}
		})
	}
}
