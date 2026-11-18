// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ysicing/51code/internal/gitrepo"
)

type LocalRepository struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	GroupID string `json:"groupID"`
	Branch  string `json:"branch"`
	Changes int    `json:"changes"`
	Problem string `json:"problem"`
}

type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Library struct {
	Repositories []LocalRepository `json:"repositories"`
	Groups       []Group           `json:"groups"`
}

func emptyLibrary() Library {
	return Library{Repositories: []LocalRepository{}, Groups: []Group{}}
}

func copyLibrary(library Library) Library {
	return Library{
		Repositories: append([]LocalRepository{}, library.Repositories...),
		Groups:       append([]Group{}, library.Groups...),
	}
}

func (manager *Manager) Library() Library {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return copyLibrary(manager.library)
}

// ErrPartial 表示操作在超时或取消前只完成了一部分；与它一同返回的结果仍然有效，可直接展示。
var ErrPartial = errors.New("操作未全部完成")

// errUnchanged 由 update 的 change 返回，表示无需写盘。
var errUnchanged = errors.New("unchanged")

// update 是所有修改的唯一入口：持 writeMu 期间以最新已保存状态的副本调用 change 并落盘，
// 落盘成功后才在 mu 内替换内存。无须落盘时也可执行 commit，同步内存状态。
// mu 只在复制和替换时短暂持有，读取方不会被磁盘写入和 fsync 阻塞；
// 写入互斥保证后发起的修改总是基于先完成的结果，不会被覆盖。
func (manager *Manager) update(change func(next *Library) error, commit func()) (Library, error) {
	manager.writeMu.Lock()
	defer manager.writeMu.Unlock()
	next := manager.Library()
	changeErr := change(&next)
	if changeErr != nil && !errors.Is(changeErr, errUnchanged) {
		return Library{}, changeErr
	}
	if changeErr == nil {
		if err := manager.persist(next); err != nil {
			return Library{}, err
		}
	}
	manager.mu.Lock()
	if changeErr == nil {
		manager.library = copyLibrary(next)
	}
	if commit != nil {
		commit()
	}
	manager.mu.Unlock()
	return next, nil
}

// persist 只能在持有 writeMu 时调用，不访问 manager.library。
func (manager *Manager) persist(next Library) error {
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(manager.file), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(manager.file), ".library-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	// rename 前落盘，避免断电后留下空文件，下次启动被当作损坏记录。
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), manager.file)
}

// snapshotWorkers 限制同时读取的仓库数：每个快照启动多个 Git 进程，并发过高会挤占共享机器与前台操作。
const snapshotWorkers = 2

// snapshotRepository 打开仓库并读取摘要；测试可替换以模拟慢仓库。
var snapshotRepository = func(ctx context.Context, path string) (gitrepo.Snapshot, error) {
	repo, err := gitrepo.Open(ctx, path)
	if err != nil {
		return gitrepo.Snapshot{}, err
	}
	return repo.Snapshot(ctx)
}

// forEachConcurrently 以 snapshotWorkers 的并发度对 [0, count) 调用 work；
// ctx 结束后不再派发新任务，但会等待已开始的调用返回。
func forEachConcurrently(ctx context.Context, count int, work func(index int)) {
	indexes := make(chan int)
	var workers sync.WaitGroup
	for range min(snapshotWorkers, count) {
		workers.Go(func() {
			for index := range indexes {
				work(index)
			}
		})
	}
dispatch:
	for index := range count {
		select {
		case indexes <- index:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(indexes)
	workers.Wait()
}

func entryFromSnapshot(snapshot gitrepo.Snapshot) LocalRepository {
	return LocalRepository{Name: snapshot.Name, Path: snapshot.Path, Branch: snapshot.Branch, Changes: len(snapshot.Files)}
}

func updateEntry(library *Library, snapshot gitrepo.Snapshot) {
	entry := entryFromSnapshot(snapshot)
	for index, current := range library.Repositories {
		if current.Path == snapshot.Path {
			entry.GroupID = current.GroupID
			library.Repositories[index] = entry
			return
		}
	}
	library.Repositories = append(library.Repositories, entry)
}

func (manager *Manager) Remember(snapshot gitrepo.Snapshot) error {
	found := false
	_, err := manager.update(func(next *Library) error {
		for index, entry := range next.Repositories {
			if entry.Path == snapshot.Path {
				found = true
				replacement := entryFromSnapshot(snapshot)
				replacement.GroupID = entry.GroupID
				if entry == replacement {
					return errUnchanged
				}
				next.Repositories[index] = replacement
				return nil
			}
		}
		return errUnchanged
	}, func() {
		if found {
			// 即使摘要相同，这次观测也比已经开始的库刷新更新。
			manager.rememberVersion++
			manager.rememberedAt[snapshot.Path] = manager.rememberVersion
		}
	})
	return err
}

func hasGroup(library Library, id string) bool {
	if id == "" {
		return true
	}
	for _, group := range library.Groups {
		if group.ID == id {
			return true
		}
	}
	return false
}

func (manager *Manager) Add(ctx context.Context, paths []string, groupID string) (Library, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if len(paths) == 0 || len(paths) > 2000 {
		return Library{}, errors.New("请选择 1 至 2000 个本地仓库")
	}
	if !hasGroup(manager.Library(), groupID) {
		return Library{}, errors.New("仓库组不存在，请重新选择")
	}
	snapshots := []gitrepo.Snapshot{}
	seen := map[string]bool{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return Library{}, err
		}
		repo, err := gitrepo.Open(ctx, path)
		if err != nil {
			return Library{}, fmt.Errorf("%s：%w", path, err)
		}
		if seen[repo.Path] {
			continue
		}
		snapshot, err := repo.Snapshot(ctx)
		if err != nil {
			return Library{}, err
		}
		seen[repo.Path] = true
		snapshots = append(snapshots, snapshot)
	}
	return manager.update(func(next *Library) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !hasGroup(*next, groupID) {
			return errors.New("仓库组不存在，请重新选择")
		}
		for _, snapshot := range snapshots {
			exists := false
			for _, entry := range next.Repositories {
				exists = exists || entry.Path == snapshot.Path
			}
			updateEntry(next, snapshot)
			if !exists {
				next.Repositories[len(next.Repositories)-1].GroupID = groupID
			}
		}
		return nil
	}, nil)
}

// RefreshLibrary 以并发度 2 读取各仓库状态。超时或取消时保存并返回已完成的部分，
// 未刷新的条目保留原值，错误包装 ErrPartial 与 ctx 的错误；仅写盘失败时返回空 Library。
func (manager *Manager) RefreshLibrary(ctx context.Context) (Library, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	manager.mu.Lock()
	entries := append([]LocalRepository(nil), manager.library.Repositories...)
	rememberVersion := manager.rememberVersion
	manager.mu.Unlock()
	refreshed := make([]bool, len(entries))
	// 各协程只写自己的下标，无需加锁。
	forEachConcurrently(ctx, len(entries), func(index int) {
		snapshot, err := snapshotRepository(ctx, entries[index].Path)
		if err == nil {
			entries[index] = entryFromSnapshot(snapshot)
		} else if ctx.Err() != nil {
			// 被超时或取消打断的失败不代表仓库有问题，保留原有信息。
			return
		} else {
			entries[index].Problem = err.Error()
		}
		refreshed[index] = true
	})
	updated := map[string]LocalRepository{}
	for index, entry := range entries {
		if refreshed[index] {
			updated[entry.Path] = entry
		}
	}
	library, err := manager.update(func(next *Library) error {
		changed := false
		for index, current := range next.Repositories {
			entry, ok := updated[current.Path]
			if !ok || manager.rememberedAt[current.Path] > rememberVersion {
				continue
			}
			entry.GroupID = current.GroupID
			if current != entry {
				next.Repositories[index] = entry
				changed = true
			}
		}
		if !changed {
			return errUnchanged
		}
		return nil
	}, nil)
	if err != nil {
		return Library{}, err
	}
	if stop := ctx.Err(); stop != nil {
		return library, fmt.Errorf("%w，已刷新 %d/%d 个仓库：%w", ErrPartial, len(updated), len(entries), stop)
	}
	return library, nil
}

func (manager *Manager) CreateGroup(name string) (Library, error) {
	return manager.editGroup("", name)
}

func (manager *Manager) RenameGroup(id, name string) (Library, error) {
	if id == "" {
		return Library{}, errors.New("请选择仓库组")
	}
	return manager.editGroup(id, name)
}

func (manager *Manager) editGroup(id, name string) (Library, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 80 || strings.ContainsAny(name, "\x00\r\n") {
		return Library{}, errors.New("仓库组名称需要 1 至 80 个字符")
	}
	return manager.update(func(next *Library) error {
		for _, group := range next.Groups {
			if group.ID != id && strings.EqualFold(group.Name, name) {
				return errors.New("已存在同名仓库组")
			}
		}
		if id == "" {
			random := make([]byte, 12)
			if _, err := rand.Read(random); err != nil {
				return err
			}
			next.Groups = append(next.Groups, Group{ID: hex.EncodeToString(random), Name: name})
			return nil
		}
		for index, group := range next.Groups {
			if group.ID == id {
				next.Groups[index].Name = name
				return nil
			}
		}
		return errors.New("仓库组不存在")
	}, nil)
}

func (manager *Manager) RemoveGroup(id string) (Library, error) {
	return manager.update(func(next *Library) error {
		if id == "" || !hasGroup(*next, id) {
			return errors.New("仓库组不存在")
		}
		groups := []Group{}
		for _, group := range next.Groups {
			if group.ID != id {
				groups = append(groups, group)
			}
		}
		next.Groups = groups
		for index := range next.Repositories {
			if next.Repositories[index].GroupID == id {
				next.Repositories[index].GroupID = ""
			}
		}
		return nil
	}, nil)
}

func (manager *Manager) Move(path, groupID string) (Library, error) {
	return manager.update(func(next *Library) error {
		if !hasGroup(*next, groupID) {
			return errors.New("仓库组不存在")
		}
		for index, entry := range next.Repositories {
			if entry.Path == path {
				next.Repositories[index].GroupID = groupID
				return nil
			}
		}
		return errors.New("仓库不在本地列表中")
	}, nil)
}

func (manager *Manager) Remove(path string) (Library, error) {
	return manager.update(func(next *Library) error {
		repositories := []LocalRepository{}
		for _, entry := range next.Repositories {
			if entry.Path != path {
				repositories = append(repositories, entry)
			}
		}
		if len(repositories) == len(next.Repositories) {
			return errors.New("仓库不在本地列表中")
		}
		next.Repositories = repositories
		return nil
	}, func() {
		delete(manager.open, path)
		delete(manager.rememberedAt, path)
	})
}
