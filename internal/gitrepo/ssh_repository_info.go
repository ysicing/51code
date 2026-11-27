// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"strings"
	"time"
)

func (repo SSHWorkspace) LostCommits(ctx context.Context) (LostCommits, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return readLostCommits(ctx, repo.head, repo.records, repo.gitTo)
}

func (repo SSHWorkspace) Stashes(ctx context.Context) ([]Stash, error) {
	output, err := repo.read(ctx, "stash", "list", "-z", "--format=%H%x00%gs%x00%cI")
	if err != nil {
		return nil, err
	}
	return parseStashes(output)
}

func (repo SSHWorkspace) StashDiff(ctx context.Context, hash string) (Diff, error) {
	if err := validCommitHash(hash); err != nil {
		return Diff{}, err
	}
	output, err := repo.read(ctx, "stash", "show", "--patch", "--no-color", "--include-untracked", "--no-ext-diff", "--no-textconv", hash)
	return sshPatch(output), err
}

func (repo SSHWorkspace) Worktrees(ctx context.Context) ([]Worktree, error) {
	output, err := repo.read(ctx, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	items, err := parseWorktreePaths(output, validSSHPath)
	if err != nil {
		return nil, err
	}
	root, err := repo.RootPath(ctx)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Current = items[index].Path == root
	}
	return items, nil
}

func (repo SSHWorkspace) RootPath(ctx context.Context) (string, error) {
	root, err := repo.read(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root = strings.TrimSuffix(root, "\n")
	if !validSSHPath(root) {
		return "", errors.New("远端仓库路径无效")
	}
	return root, nil
}

func (repo SSHWorkspace) Submodules(ctx context.Context) ([]Submodule, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	items, err := readSubmoduleIndex(ctx, "", repo.records)
	if err != nil || len(items) == 0 {
		return items, err
	}
	byPath := make(map[string]int, len(items))
	for index, item := range items {
		byPath[item.Path] = index
	}
	command := `head=$(git rev-parse --verify HEAD) || exit 1
dirty=$(git --no-optional-locks -c core.fsmonitor=false status --porcelain --ignore-submodules=none) || exit 1
printf '%s\000%s\000%s\000' "$sm_path" "$head" "${dirty:+dirty}"`
	output, err := repo.read(ctx, "submodule", "foreach", "--quiet", command)
	if err != nil {
		return nil, err
	}
	if output == "" {
		return items, nil
	}
	fields := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(fields)%3 != 0 {
		return nil, errors.New("无法解析远端子模块状态")
	}
	for index := 0; index < len(fields); index += 3 {
		if pos, ok := byPath[fields[index]]; ok {
			if err := validCommitHash(fields[index+1]); err != nil {
				return nil, err
			}
			items[pos].Head, items[pos].Dirty = fields[index+1], fields[index+2] != ""
		}
	}
	return items, nil
}
