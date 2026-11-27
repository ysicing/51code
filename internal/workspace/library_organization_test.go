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
		if _, err := manager.RememberSSH(t.Context(), repo, ""); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	library := reloaded.Library()
	if len(library.Repositories) != 0 || len(library.SSHRepositories) != 2 ||
		library.SSHRepositories[0].SSHWorkspace != second || library.SSHRepositories[1].SSHWorkspace != first ||
		library.SSHRepositories[1].LastOpenedAt < library.SSHRepositories[0].LastOpenedAt {
		t.Fatalf("远端记录未正确保存: %+v", library)
	}
	remaining, err := reloaded.ForgetSSH(first)
	if err != nil || len(remaining.SSHRepositories) != 1 || remaining.SSHRepositories[0].SSHWorkspace != second {
		t.Fatalf("移除影响其他主机的同名仓库: %+v, %v", remaining, err)
	}
}

func TestSSHCanonicalOpenConsolidatesLegacyGroupRecords(t *testing.T) {
	file := filepath.Join(t.TempDir(), "library.json")
	raw := `{"groups":[{"id":"team","name":"Team"},{"id":"later","name":"Later"}],"sshRepositories":[{"host":"dev","path":"/srv/app/subdir/","groupID":"team"},{"host":"dev","path":"/srv/app"},{"host":"dev","path":"/srv/app/.","groupID":"later","lastOpenedAt":9000000000000000000},{"host":"staging","path":"/srv/app"},{"host":"dev","path":"/srv/other-worktree","groupID":"team"}]}`
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(file)
	if err != nil {
		t.Fatal(err)
	}
	canonical := gitrepo.SSHWorkspace{Host: "dev", Path: "/srv/app"}
	alias := gitrepo.SSHWorkspace{Host: canonical.Host, Path: "/srv/app/subdir"}
	library, err := manager.OpenSSH(t.Context(), alias, canonical, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(library.SSHRepositories) != 3 || library.SSHRepositories[0].SSHWorkspace != canonical ||
		library.SSHRepositories[0].GroupID != "team" || library.SSHRepositories[0].LastOpenedAt != 9000000000000000000 {
		t.Fatalf("旧条目未按工作树合并并保留分组: %+v", library.SSHRepositories)
	}
}

func TestSSHOpenRootPreservesCanonicalGroupAndRejectsInvalidDestination(t *testing.T) {
	file := filepath.Join(t.TempDir(), "library.json")
	raw := `{"groups":[{"id":"root","name":"Root"},{"id":"alias","name":"Alias"}],"sshRepositories":[{"host":"dev","path":"/child/..","groupID":"alias"},{"host":"dev","path":"/","groupID":"root"},{"host":"dev","path":""}]}`
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(file)
	if err != nil {
		t.Fatal(err)
	}
	root := gitrepo.SSHWorkspace{Host: "dev", Path: "/"}
	library, err := manager.OpenSSH(t.Context(), root, root, "")
	if err != nil || len(library.SSHRepositories) != 3 || library.SSHRepositories[0].Path != "/child/.." ||
		library.SSHRepositories[1].SSHWorkspace != root || library.SSHRepositories[1].GroupID != "root" || library.SSHRepositories[2].Path != "" {
		t.Fatalf("打开根目录应保留独立旧条目: %+v, %v", library.SSHRepositories, err)
	}
	for _, invalid := range []gitrepo.SSHWorkspace{
		{Host: "", Path: "/"}, {Host: "-V", Path: "/"}, {Host: "dev;extra", Path: "/"},
		{Host: "dev", Path: ""}, {Host: "dev", Path: "relative"},
		{Host: "dev", Path: "/x\x00"}, {Host: "dev", Path: "/x\r"}, {Host: "dev", Path: "/x\n"},
	} {
		if _, err := manager.OpenSSH(t.Context(), root, invalid, ""); err == nil {
			t.Errorf("接受了无效远端地址: %+v", invalid)
		}
	}
	if !reflect.DeepEqual(library, manager.Library()) {
		t.Fatal("无效地址修改了仓库库")
	}
}

func TestSSHOpenMergesOnlyConfirmedParentPath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "library.json")
	raw := `{"groups":[{"id":"team","name":"Team"}],"sshRepositories":[{"host":"dev","path":"/left","groupID":"team"},{"host":"dev","path":"/left/link/.."}]}`
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(file)
	if err != nil {
		t.Fatal(err)
	}
	library, err := manager.OpenSSH(t.Context(),
		gitrepo.SSHWorkspace{Host: "dev", Path: "/left/link/.."},
		gitrepo.SSHWorkspace{Host: "dev", Path: "/right"}, "")
	if err != nil || len(library.SSHRepositories) != 2 ||
		library.SSHRepositories[0].Path != "/left" || library.SSHRepositories[0].GroupID != "team" ||
		library.SSHRepositories[1].Path != "/right" || library.SSHRepositories[1].GroupID != "" {
		t.Fatalf("远端已确认的物理路径应只替换原始别名: %+v, %v", library.SSHRepositories, err)
	}
}

func TestSSHLibraryDecodeClearsMissingGroups(t *testing.T) {
	file := filepath.Join(t.TempDir(), "library.json")
	raw := `{"groups":[{"id":"team","name":"Team"}],"defaultGroupID":"missing","sshRepositories":[{"host":"dev","path":"/first","groupID":"missing"},{"host":"dev","path":"/second","groupID":"team"}]}`
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(file)
	if err != nil {
		t.Fatal(err)
	}
	library := manager.Library()
	if library.DefaultGroupID != "" || len(library.SSHRepositories) != 2 ||
		library.SSHRepositories[0].GroupID != "" || library.SSHRepositories[1].GroupID != "team" {
		t.Fatalf("加载时应仅清理悬空分组: %+v", library)
	}
}

func TestSSHRepositoryGroupSurvivesReconnectAndReload(t *testing.T) {
	store := filepath.Join(t.TempDir(), "library.json")
	if err := os.WriteFile(store, []byte(`{"groups":[{"id":"team","name":"Team"}],"defaultGroupID":"team","sshRepositories":[{"host":"legacy","path":"/srv/api"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	legacy := gitrepo.SSHWorkspace{Host: "legacy", Path: "/srv/api"}
	second := gitrepo.SSHWorkspace{Host: "staging", Path: legacy.Path}
	if _, err := manager.RememberSSH(t.Context(), legacy, "team"); err != nil {
		t.Fatal(err)
	}
	library, err := manager.RememberSSH(t.Context(), second, "team")
	if err != nil || len(library.SSHRepositories) != 2 || library.SSHRepositories[1].GroupID != "" {
		t.Fatalf("重连改变了旧记录或分组: %+v, %v", library, err)
	}
	if _, err := manager.MoveSSH(legacy, "missing"); err == nil {
		t.Fatal("接受了失效的分组")
	}
	manager, err = New(store)
	if err != nil {
		t.Fatal(err)
	}
	if local, remote := manager.Library().GroupCounts("team"); local != 0 || remote != 1 {
		t.Fatalf("重新加载后分组数量错误: %d, %d", local, remote)
	}
	if _, err := manager.MoveSSH(legacy, "team"); err != nil {
		t.Fatal(err)
	}
	library, err = manager.RemoveGroup("team")
	if err != nil {
		t.Fatal(err)
	}
	if _, remote := library.GroupCounts(""); remote != 2 || library.DefaultGroupID != "" {
		t.Fatalf("删除分组未还原到未分组: %+v", library)
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
