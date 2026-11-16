// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import "context"

// WatchDirectories 返回工作区、独立 Git 目录和共享 Git 目录，兼容链接工作树。
func (repo *Repository) WatchDirectories(ctx context.Context) ([]string, error) {
	directories, err := repo.metadataDirectories(ctx)
	if err != nil {
		return nil, err
	}
	return append([]string{repo.Path}, directories...), nil
}

// FetchInBackground 可被前台操作取消；禁用附带维护，避免定时获取占用额外资源。
func (repo *Repository) FetchInBackground(ctx context.Context) error {
	if err := repo.lockHistory(ctx); err != nil {
		return err
	}
	defer repo.mu.Unlock()
	return repo.runRemote(ctx, "-c", "fetch.writeCommitGraph=false", "-c", "gc.auto=0",
		"-c", "maintenance.auto=false", "fetch", "--all")
}
