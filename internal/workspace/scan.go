// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
)

type ScanResult struct {
	Repositories []LocalRepository `json:"repositories"`
	Warnings     []string          `json:"warnings"`
	Truncated    bool              `json:"truncated"`
}

func (manager *Manager) Scan(ctx context.Context, root string) (ScanResult, error) {
	return manager.scan(ctx, root, (*gitrepo.Repository).Snapshot)
}

func (manager *Manager) scan(ctx context.Context, root string, readSnapshot func(*gitrepo.Repository, context.Context) (gitrepo.Snapshot, error)) (ScanResult, error) {
	result := ScanResult{Repositories: []LocalRepository{}, Warnings: []string{}}
	if strings.TrimSpace(root) == "" {
		return result, errors.New("请选择扫描文件夹")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return result, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return result, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, errors.New("请选择文件夹")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// 遍历在当前协程进行，仓库快照交给 snapshotWorkers 个协程；mu 保护两边都会写的 Warnings、
	// Repositories 与 seen。Truncated 只由遍历协程写入。
	var mu sync.Mutex
	visited, candidates := 0, 0
	seen := map[string]bool{}
	warn := func(path string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if len(result.Warnings) < 20 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s：%s", path, err))
		}
	}
	paths := make(chan string)
	verifiedRoots := make(chan bool)
	var workers sync.WaitGroup
	for range snapshotWorkers {
		workers.Go(func() {
			for path := range paths {
				repo, err := gitrepo.Open(ctx, path)
				isRoot := err == nil && repo.Path == path
				verifiedRoots <- isRoot
				if !isRoot {
					if err != nil && ctx.Err() == nil {
						warn(path, err)
					}
					continue
				}
				snapshot, err := readSnapshot(repo, ctx)
				if err != nil {
					// 被超时或取消打断的失败不是仓库问题，不作为警告展示。
					if ctx.Err() == nil {
						warn(path, err)
					}
					continue
				}
				if snapshot.LinkedWorktree {
					continue
				}
				mu.Lock()
				if snapshot.Path == path && !seen[path] {
					seen[path] = true
					result.Repositories = append(result.Repositories, entryFromSnapshot(snapshot))
				}
				mu.Unlock()
			}
		})
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			warn(path, walkErr)
			return fs.SkipDir
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root {
			name := entry.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return fs.SkipDir
			}
			relative, _ := filepath.Rel(root, path)
			if len(strings.Split(relative, string(filepath.Separator))) > 12 {
				result.Truncated = true
				return fs.SkipDir
			}
		}
		visited++
		if visited > 20000 {
			result.Truncated = true
			return fs.SkipAll
		}
		if _, err := os.Lstat(filepath.Join(path, ".git")); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				warn(path, err)
			}
			return nil
		}
		// 无效的 .git 也会消耗探测预算。
		if candidates >= 2000 {
			result.Truncated = true
			return fs.SkipAll
		}
		candidates++
		select {
		case paths <- path:
		case <-ctx.Done():
			return ctx.Err()
		}
		// 只等待 worker 确认仓库根，快照可与遍历并行。进入已确认仓库的内部会让构建产物耗尽目录预算。
		if <-verifiedRoots {
			return fs.SkipDir
		}
		return ctx.Err()
	})
	close(paths)
	workers.Wait()
	sort.Slice(result.Repositories, func(first, second int) bool {
		return result.Repositories[first].Path < result.Repositories[second].Path
	})
	if stop := ctx.Err(); stop != nil {
		// 超时或取消时返回已完成快照的仓库，供界面展示部分结果。
		result.Truncated = true
		return result, fmt.Errorf("%w，已列出 %d 个仓库：%w", ErrPartial, len(result.Repositories), stop)
	}
	if err != nil {
		return result, err
	}
	return result, nil
}
