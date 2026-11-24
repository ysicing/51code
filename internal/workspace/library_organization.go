// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
)

// OrderedRepositories 返回用于展示的副本；旧记录和时间相同的记录保留原有顺序。
func (library Library) OrderedRepositories(order string) []LocalRepository {
	entries := slices.Clone(library.Repositories)
	if order == "recent" {
		slices.SortStableFunc(entries, func(first, second LocalRepository) int {
			return cmp.Compare(second.LastOpenedAt, first.LastOpenedAt)
		})
	}
	return entries
}

// MarkOpened 只由实际打开成功的入口调用；时间使用 Unix 纳秒，零值表示旧记录或尚未打开。
func (manager *Manager) MarkOpened(ctx context.Context, path string) (Library, error) {
	return manager.update(func(next *Library) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for index, entry := range next.Repositories {
			if entry.Path == path {
				if entry.linkedWorktree {
					return errUnchanged
				}
				next.Repositories[index].LastOpenedAt = max(time.Now().UnixNano(), entry.LastOpenedAt)
				return nil
			}
		}
		if _, err := manager.Get(path); err == nil {
			return errUnchanged
		}
		return errors.New("仓库不在本地列表中")
	}, nil)
}

// Relocate 接收已选定的规范工作区根路径，只替换库中的路径，不移动文件或打开仓库。
// 校验和提交均拒绝过期条目，目标路径变化时不能沿用针对旧目标准备的偏好映射。
func (manager *Manager) Relocate(ctx context.Context, expected LocalRepository, path string) (Library, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Library{}, err
	}
	if !slices.Contains(manager.Library().Repositories, expected) {
		return Library{}, errors.New("仓库记录已变更，请刷新列表后重试")
	}
	if _, err := gitrepo.Open(ctx, expected.Path); err == nil {
		return Library{}, errors.New("原路径仍是有效 Git 工作区，请刷新仓库列表后重试")
	}
	snapshot, err := snapshotRepository(ctx, path)
	if err != nil {
		return Library{}, err
	}
	if snapshot.LinkedWorktree {
		return Library{}, ErrLinkedWorktree
	}
	if snapshot.Path == expected.Path {
		return Library{}, errors.New("请选择仓库的新位置")
	}
	if snapshot.Path != path {
		return Library{}, errors.New("所选仓库位置已变更，请重新选择")
	}
	if _, err := gitrepo.Open(ctx, expected.Path); err == nil {
		return Library{}, errors.New("原路径仍是有效 Git 工作区，请刷新仓库列表后重试")
	}
	return manager.update(func(next *Library) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, entry := range next.Repositories {
			if entry.Path == snapshot.Path {
				return errors.New("所选仓库已在本地列表中")
			}
		}
		for index, current := range next.Repositories {
			if current.Path != expected.Path {
				continue
			}
			if current != expected {
				return errors.New("仓库记录已变更，请刷新列表后重试")
			}
			entry := entryFromSnapshot(snapshot)
			entry.GroupID, entry.LastOpenedAt = current.GroupID, current.LastOpenedAt
			next.Repositories[index] = entry
			return nil
		}
		return errors.New("仓库不在本地列表中")
	}, func() {
		delete(manager.open, expected.Path)
		delete(manager.rememberedAt, expected.Path)
	})
}
