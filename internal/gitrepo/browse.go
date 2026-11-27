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
	return browsePaths(ctx, revision, hash, validBrowsePath, func(ctx context.Context, addPath func(string) error, args ...string) error {
		return runRecords(ctx, repo.Path, 30*time.Second, addPath, args...)
	})
}

func browsePaths(ctx context.Context, revision, hash string, valid func(string) error,
	records func(context.Context, func(string) error, ...string) error) ([]string, error) {
	paths := map[string]bool{}
	addPath := func(path string) error {
		if path != "" {
			paths[strings.TrimSuffix(path, "/")] = true
		}
		return nil
	}
	if revision == "WORKTREE" || revision == "INDEX" {
		args := []string{"ls-files", "-z", "--cached"}
		if revision == "WORKTREE" {
			args = append(args, "--others", "--exclude-standard")
		}
		if err := records(ctx, addPath, append(args, "--")...); err != nil {
			return nil, err
		}
	}
	if hash != "" && revision != "INDEX" {
		if err := records(ctx, addPath, "ls-tree", "-r", "--name-only", "-z", hash, "--"); err != nil {
			return nil, err
		}
	}
	files := make([]string, 0, len(paths))
	for path := range paths {
		if err := valid(path); err != nil {
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
	side := newImageAwareFileContent(path, revision)
	side.blameBase = hash
	switch revision {
	case "WORKTREE":
		if err := checkBrowseWorktreeMember(ctx, repo.git, path, hash); err != nil {
			return nil, err
		}
		return repo.readWorktreeFileContent(ctx, side)
	case "INDEX":
		content, err := repo.readIndexFileContent(ctx, side)
		if err == nil && !content.Present {
			return nil, errors.New("暂存区不存在该文件")
		}
		if err == nil {
			content.blameBase, err = repo.head(ctx)
		}
		return content, err
	default:
		return repo.readTreeFileContent(ctx, side, hash)
	}
}

func checkBrowseWorktreeMember(ctx context.Context, git func(context.Context, ...string) (string, error), path, hash string) error {
	output, err := git(ctx, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", path)
	if err != nil {
		return err
	}
	for name := range strings.SplitSeq(output, "\x00") {
		if strings.TrimSuffix(name, "/") == path {
			return nil
		}
	}
	if hash != "" {
		output, err = git(ctx, "ls-tree", "-z", hash, "--", path)
		if err != nil {
			return err
		}
		for record := range strings.SplitSeq(output, "\x00") {
			metadata, name, ok := strings.Cut(record, "\t")
			fields := strings.Fields(metadata)
			if ok && name == path && len(fields) == 3 && fields[1] != "tree" {
				return nil
			}
		}
	}
	return errors.New("文件不在仓库浏览范围内")
}

func (repo *Repository) browseRevision(ctx context.Context, revision string) (string, error) {
	return resolveBrowseRevision(ctx, revision, repo.head)
}

func resolveBrowseRevision(ctx context.Context, revision string, head func(context.Context) (string, error)) (string, error) {
	switch revision {
	case "HEAD", "WORKTREE":
		return head(ctx)
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
