// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
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

// changeCacheBytes 限制缓存文件列表的估算内存；超大提交只缓存父提交，文件列表每次重新读取。
const changeCacheBytes = 16 << 20

func (repo *Repository) rememberChanges(key string, files []CommitFile) {
	repo.rememberCommitChanges(key, files, nil)
}

func (repo *Repository) rememberCommitChanges(key string, files []CommitFile, parents []string) {
	size := 0
	for _, file := range files {
		// 每项按路径长度加结构体及字符串头的固定开销估算。
		size += len(file.Path) + len(file.Status) + len(file.OriginalPath) + 80
	}
	cache := changeCache{key: key, parents: slices.Clone(parents)}
	if size <= changeCacheBytes {
		cache.files = slices.Clone(files)
		if cache.files == nil {
			cache.files = []CommitFile{}
		}
	}
	repo.changes = cache
}

func (repo *Repository) cachedChanges(key string) ([]CommitFile, bool) {
	if repo.changes.key != key || repo.changes.files == nil {
		return nil, false
	}
	return repo.changes.files, true
}

// commitChanges 优先复用 CommitFiles 刚读取的同一提交列表，省去重复读取。
func (repo *Repository) commitChanges(ctx context.Context, hash string) ([]CommitFile, error) {
	if files, ok := repo.cachedChanges(commitChangesKey(hash)); ok {
		return files, nil
	}
	files, parents, err := repo.commitFiles(ctx, hash)
	if err == nil {
		repo.rememberCommitChanges(commitChangesKey(hash), files, parents)
	}
	return files, err
}

// commitParents 返回提交自身及父提交 ID；读取文件列表时已一并取得，同一提交切换文件时不再启动 Git。
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
// owner 区分仓库实例，不同实例互不命中。
type cachedBlob struct {
	owner   *Repository
	hash    string
	content string
}

// blob 缓存由所有仓库共享总量上限：按仓库各自缓存时，切换过的仓库会一直占用内存。
// 最多 blobCacheEntries 项、总计 blobCacheBytes 字节，超出时淘汰最久未使用的项。
// 使用独立的锁：调用方持有各自仓库的 mu，这里不会反向获取任何仓库锁。
const (
	blobCacheEntries = 8
	blobCacheBytes   = 4 * fileContentLimit
)

var blobCache struct {
	sync.Mutex
	entries []cachedBlob
	bytes   int
}

func (repo *Repository) cachedBlob(hash string) (string, bool) {
	blobCache.Lock()
	defer blobCache.Unlock()
	for index, blob := range blobCache.entries {
		if blob.owner == repo && blob.hash == hash {
			// 移到末尾，表示最近使用。
			blobCache.entries = append(slices.Delete(blobCache.entries, index, index+1), blob)
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
	blobCache.Lock()
	defer blobCache.Unlock()
	for len(blobCache.entries) > 0 &&
		(len(blobCache.entries) >= blobCacheEntries || blobCache.bytes+len(content) > blobCacheBytes) {
		blobCache.bytes -= len(blobCache.entries[0].content)
		blobCache.entries = slices.Delete(blobCache.entries, 0, 1)
	}
	blobCache.entries = append(blobCache.entries, cachedBlob{owner: repo, hash: hash, content: content})
	blobCache.bytes += len(content)
}
