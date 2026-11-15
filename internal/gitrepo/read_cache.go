// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// changeCache 保存最近一次提交或比较的文件列表，供逐个查看文件时复用。
// 键只由完整提交 ID 组成，对象内容不可变；CommitFiles 与 Compare 每次都重新读取并覆盖，
// 因此文件差异始终与界面当前展示的列表一致，不会长期沿用旧的重命名配置。
type changeCache struct {
	key     string
	files   []CommitFile
	parents []string
}

func commitChangesKey(hash string) string {
	return "commit\x00" + hash
}

func compareChangesKey(from, target string) string {
	return "compare\x00" + from + "\x00" + target
}

func (repo *Repository) rememberChanges(key string, files []CommitFile) {
	repo.changes = changeCache{key: key, files: slices.Clone(files)}
}

func (repo *Repository) cachedChanges(key string) ([]CommitFile, bool) {
	if repo.changes.key != key || repo.changes.files == nil {
		return nil, false
	}
	return repo.changes.files, true
}

// commitChanges 优先复用 CommitFiles 刚读取的同一提交列表，省去重复的类型检查与 diff-tree。
func (repo *Repository) commitChanges(ctx context.Context, hash string) ([]CommitFile, error) {
	if files, ok := repo.cachedChanges(commitChangesKey(hash)); ok {
		return files, nil
	}
	files, err := repo.commitFiles(ctx, hash)
	if err == nil {
		repo.rememberChanges(commitChangesKey(hash), files)
	}
	return files, err
}

// commitParents 返回提交自身及父提交 ID；同一提交切换文件时只读取一次。
func (repo *Repository) commitParents(ctx context.Context, hash string) ([]string, error) {
	key := commitChangesKey(hash)
	if repo.changes.key == key && repo.changes.parents != nil {
		return repo.changes.parents, nil
	}
	// 固定实际提交及第一父提交，不把可变引用或路径表达式交给 cat-file。
	output, err := repo.git(ctx, "rev-list", "--parents", "-n", "1", hash, "--")
	if err != nil {
		return nil, err
	}
	parents := strings.Fields(output)
	if len(parents) == 0 {
		return nil, errors.New("无法读取提交及父提交")
	}
	if repo.changes.key == key {
		repo.changes.parents = parents
	}
	return parents, nil
}

// cachedBlob 按对象 ID 缓存小文件内容；Diff 与 Patch 会先后读取同一索引/HEAD blob。
type cachedBlob struct {
	hash    string
	content string
}

// 每个仓库最多保留 4 个不超过 fileContentLimit 的 blob，内存上限约 4 MiB。
const blobCacheEntries = 4

func (repo *Repository) cachedBlob(hash string) (string, bool) {
	for _, blob := range repo.blobs {
		if blob.hash == hash {
			return blob.content, true
		}
	}
	return "", false
}

func (repo *Repository) rememberBlob(hash, content string) {
	if hash == "" || len(content) > fileContentLimit {
		return
	}
	if _, ok := repo.cachedBlob(hash); ok {
		return
	}
	if len(repo.blobs) >= blobCacheEntries {
		repo.blobs = slices.Delete(repo.blobs, 0, 1)
	}
	repo.blobs = append(repo.blobs, cachedBlob{hash: hash, content: content})
}
