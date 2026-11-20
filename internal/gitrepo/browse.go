// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// BrowseFiles 返回完整仓库的文件路径；WORKTREE 同时保留 HEAD 中已删除的文件入口。
func (repo *Repository) BrowseFiles(ctx context.Context, revision string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	hash, err := repo.browseRevision(ctx, revision)
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	if revision == "WORKTREE" || revision == "INDEX" {
		args := []string{"ls-files", "-z", "--cached"}
		if revision == "WORKTREE" {
			args = append(args, "--others", "--exclude-standard")
		}
		output, err := repo.git(ctx, append(args, "--")...)
		if err != nil {
			return nil, err
		}
		for _, path := range strings.Split(output, "\x00") {
			if path != "" {
				paths[strings.TrimSuffix(path, "/")] = true
			}
		}
	}
	if hash != "" && revision != "INDEX" {
		output, err := repo.git(ctx, "ls-tree", "-r", "--name-only", "-z", hash, "--")
		if err != nil {
			return nil, err
		}
		for _, path := range strings.Split(output, "\x00") {
			if path != "" {
				paths[path] = true
			}
		}
	}
	files := make([]string, 0, len(paths))
	for path := range paths {
		if err := validBrowsePath(path); err != nil {
			return nil, err
		}
		files = append(files, path)
	}
	slices.Sort(files)
	return files, nil
}

// BrowseFileContent 仅预览当前版本中的成员；成员校验限定到路径，不逐文件遍历仓库。
func (repo *Repository) BrowseFileContent(ctx context.Context, path, revision string) (*FileContent, error) {
	if err := validBrowsePath(path); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	hash, err := repo.browseRevision(ctx, revision)
	if err != nil {
		return nil, err
	}
	switch revision {
	case "WORKTREE":
		output, err := repo.git(ctx, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", path)
		if err != nil {
			return nil, err
		}
		member := false
		for _, name := range strings.Split(output, "\x00") {
			if strings.TrimSuffix(name, "/") == path {
				member = true
				break
			}
		}
		if !member && hash != "" {
			output, err = repo.git(ctx, "ls-tree", "-z", hash, "--", path)
			if err != nil {
				return nil, err
			}
			for _, record := range strings.Split(output, "\x00") {
				metadata, name, ok := strings.Cut(record, "\t")
				fields := strings.Fields(metadata)
				if ok && name == path && len(fields) == 3 && fields[1] != "tree" {
					member = true
					break
				}
			}
		}
		if !member {
			return nil, errors.New("文件不在仓库浏览范围内")
		}
		return repo.worktreeFileContent(ctx, path)
	case "INDEX":
		content, err := repo.indexFileContent(ctx, path)
		if err == nil && !content.Present {
			return nil, errors.New("暂存区不存在该文件")
		}
		return content, err
	default:
		return repo.treeFileContent(ctx, path, revision, hash)
	}
}

func (repo *Repository) browseRevision(ctx context.Context, revision string) (string, error) {
	switch revision {
	case "HEAD", "WORKTREE":
		return repo.head(ctx)
	case "INDEX":
		return "", ctx.Err()
	default:
		if err := validCommitHash(revision); err != nil {
			return "", err
		}
		return revision, ctx.Err()
	}
}

func validBrowsePath(path string) error {
	if err := validPath(path); err != nil {
		return err
	}
	if filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) != path {
		return errors.New("请使用仓库内的规范相对路径")
	}
	return nil
}
