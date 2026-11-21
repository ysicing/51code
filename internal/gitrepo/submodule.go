// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

type Submodule struct {
	Path, Head, Target, Problem           string
	Dirty, Conflict, InProgress, Detached bool
}

func (item Submodule) CanUpdate() bool {
	return item.Target != "" && item.Head != item.Target && item.Problem == "" &&
		!item.Dirty && !item.Conflict && !item.InProgress
}

func (repo *Repository) Submodules(ctx context.Context) ([]Submodule, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := repo.lockHistory(ctx); err != nil {
		return nil, err
	}
	defer repo.mu.Unlock()
	items, err := repo.submoduleIndex(ctx, "")
	if err != nil {
		return nil, err
	}
	for index := range items {
		repo.inspectSubmodule(ctx, &items[index])
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (repo *Repository) submoduleIndex(ctx context.Context, path string) ([]Submodule, error) {
	args := []string{"ls-files", "--stage", "-z", "--"}
	if path != "" {
		args = append(args, path)
	}
	entries := make(map[string]Submodule)
	conflicts := make(map[string]bool)
	err := runRecords(ctx, repo.Path, 30*time.Second, func(record string) error {
		metadata, name, ok := strings.Cut(record, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 {
			return errors.New("无法读取子模块索引")
		}
		if path != "" && path != name {
			return nil
		}
		if fields[2] != "0" {
			conflicts[name] = true
		}
		if fields[0] == "160000" {
			item := entries[name]
			item.Path = name
			if fields[2] == "0" {
				if err := validCommitHash(fields[1]); err != nil {
					return err
				}
				item.Target = fields[1]
			}
			entries[name] = item
		}
		return nil
	}, args...)
	if err != nil {
		return nil, err
	}
	items := make([]Submodule, 0, len(entries))
	for name, item := range entries {
		item.Conflict = conflicts[name]
		items = append(items, item)
	}
	slices.SortFunc(items, func(first, second Submodule) int { return strings.Compare(first.Path, second.Path) })
	return items, nil
}

func validSubmodulePath(path string) bool {
	local := filepath.FromSlash(path)
	return validPath(path) == nil && filepath.IsLocal(local) &&
		filepath.ToSlash(filepath.Clean(local)) == path
}

func (repo *Repository) submoduleDirectory(path string) (string, error) {
	if !validSubmodulePath(path) {
		return "", errors.New("请输入有效的仓库相对子模块路径")
	}
	directory := repo.Path
	for part := range strings.SplitSeq(path, "/") {
		if directory != repo.Path {
			if _, err := os.Lstat(filepath.Join(directory, ".git")); !os.IsNotExist(err) {
				return "", errors.New("子模块路径不能位于另一个仓库内")
			}
		}
		directory = filepath.Join(directory, part)
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", errors.New("子模块路径包含文件或符号链接")
		}
	}
	return directory, nil
}

func (repo *Repository) submoduleRepository(ctx context.Context, path string) (*Repository, error) {
	directory, err := repo.submoduleDirectory(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Join(directory, ".git"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return nil, errors.New("子模块 Git 目录无效")
	}
	child, err := Open(ctx, directory)
	if err != nil {
		return nil, err
	}
	if !worktreeSamePath(child.Path, directory) {
		return nil, errors.New("子模块目录未指向独立仓库")
	}
	return child, nil
}

func checkEmptySubmoduleDirectory(path string) error {
	directory, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer directory.Close()
	names, err := directory.Readdirnames(1)
	if len(names) != 0 {
		return errors.New("子模块目录中已有文件，请先处理这些文件")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (repo *Repository) inspectSubmodule(ctx context.Context, item *Submodule) *Repository {
	child, err := repo.submoduleRepository(ctx, item.Path)
	if err != nil {
		item.Problem = err.Error()
		return nil
	}
	if child == nil {
		if err := checkEmptySubmoduleDirectory(filepath.Join(repo.Path, filepath.FromSlash(item.Path))); err != nil {
			item.Problem = err.Error()
		}
		return nil
	}
	branch, files, err := child.streamStatus(ctx, []string{
		"status", "--porcelain=v2", "-z", "--branch", "--no-ahead-behind", "--untracked-files=all", "--ignore-submodules=none",
	})
	if err == nil {
		item.Head, item.Dirty, item.Detached = branch.head, len(files) != 0, branch.detached
		var operation Operation
		operation, err = child.operation(ctx)
		item.InProgress = operation.Kind != ""
	}
	if err != nil {
		item.Problem = err.Error()
	}
	return child
}

func (repo *Repository) SubmodulePath(ctx context.Context, path string) (string, error) {
	if !validSubmodulePath(path) {
		return "", errors.New("子模块路径无效")
	}
	if err := repo.lockHistory(ctx); err != nil {
		return "", err
	}
	defer repo.mu.Unlock()
	items, err := repo.submoduleIndex(ctx, path)
	if err != nil {
		return "", err
	}
	if len(items) != 1 {
		return "", errors.New("索引中没有此子模块，请刷新")
	}
	child, err := repo.submoduleRepository(ctx, path)
	if err != nil {
		return "", err
	}
	if child == nil {
		return "", errors.New("请先初始化子模块")
	}
	return child.Path, nil
}

func (repo *Repository) UpdateSubmodule(ctx context.Context, expected Submodule) error {
	if !validSubmodulePath(expected.Path) || validCommitHash(expected.Target) != nil {
		return errors.New("子模块路径或目标提交无效")
	}
	if err := repo.lockHistory(ctx); err != nil {
		return err
	}
	defer repo.mu.Unlock()
	if err := repo.checkSubmoduleOperation(ctx); err != nil {
		return err
	}
	items, err := repo.submoduleIndex(ctx, expected.Path)
	if err != nil {
		return err
	}
	if len(items) != 1 || items[0].Target != expected.Target || items[0].Conflict {
		return errors.New("子模块的索引记录已变化或存在冲突，请刷新")
	}
	current := items[0]
	child := repo.inspectSubmodule(ctx, &current)
	if err := ctx.Err(); err != nil {
		return err
	}
	if current.Problem != "" {
		return errors.New(current.Problem)
	}
	if current.Head != expected.Head {
		return errors.New("子模块的当前提交已变化，请刷新")
	}
	if current.Dirty || current.InProgress {
		return errors.New("请先处理子模块的本地修改或进行中的 Git 操作")
	}
	if current.Head == current.Target {
		return nil
	}
	if child != nil {
		if err := child.checkSubmoduleCheckout(ctx, current); err != nil {
			return err
		}
	}
	if err := repo.runRemote(ctx, "-c", "submodule.recurse=false", "submodule", "update", "--init", "--checkout", "--", current.Path); err != nil {
		return err
	}
	child, err = repo.submoduleRepository(ctx, current.Path)
	if err != nil {
		return err
	}
	if child == nil {
		return errors.New("Git 未初始化此子模块，请检查 .gitmodules")
	}
	head, err := child.head(ctx)
	if err != nil {
		return err
	}
	if head != current.Target {
		return errors.New("子模块未检出已确认的目标提交，请刷新后检查")
	}
	return nil
}

func (repo *Repository) checkSubmoduleCheckout(ctx context.Context, item Submodule) error {
	if item.Detached && item.Head != "" {
		refs, err := repo.git(ctx, "for-each-ref", "--count=1", "--contains="+item.Head,
			"--format=%(refname)", "refs/heads", "refs/remotes", "refs/tags")
		if err != nil {
			return err
		}
		if refs == "" {
			if _, err := repo.git(ctx, "merge-base", "--is-ancestor", item.Head, item.Target); err != nil {
				return errors.New("子模块的分离 HEAD 有未被引用保留的提交，请先在子模块中创建分支")
			}
		}
	}
	ignored, err := repo.git(ctx, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil || ignored == "" {
		return err
	}
	if _, err := repo.git(ctx, "cat-file", "-e", item.Target+"^{commit}"); err != nil {
		return errors.New("子模块有忽略文件，请先在子模块中获取目标提交，以便检查覆盖风险")
	}
	return repo.checkIgnoredPathCollisions(ctx, item.Head, item.Target, ignored)
}

func (repo *Repository) AddSubmodule(ctx context.Context, source, path string) error {
	if strings.TrimSpace(source) == "" || strings.HasPrefix(strings.TrimSpace(source), "-") ||
		strings.ContainsFunc(source, unicode.IsControl) {
		return errors.New("请输入有效的子模块仓库地址")
	}
	if err := repo.lockHistory(ctx); err != nil {
		return err
	}
	defer repo.mu.Unlock()
	if err := repo.checkSubmoduleOperation(ctx); err != nil {
		return err
	}
	directory, err := repo.submoduleDirectory(path)
	if err != nil {
		return err
	}
	if err := checkEmptySubmoduleDirectory(directory); err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(repo.Path, ".gitmodules"))
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New(".gitmodules 必须是普通文件")
		}
		if _, err := repo.git(ctx, "ls-files", "--error-unmatch", "--", ".gitmodules"); err != nil {
			return fmt.Errorf("请先处理未跟踪的 .gitmodules：%w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := repo.git(ctx, "diff", "--no-ext-diff", "--no-textconv", "--quiet", "--", ".gitmodules"); err != nil {
		if exitCode(err, 1) {
			return errors.New("请先处理 .gitmodules 中未暂存的修改")
		}
		return err
	}
	return repo.runRemote(ctx, "-c", "submodule.recurse=false", "submodule", "add", "--", source, path)
}

func (repo *Repository) checkSubmoduleOperation(ctx context.Context) error {
	operation, err := repo.operation(ctx)
	if err != nil {
		return err
	}
	if operation.Kind != "" {
		return errors.New("请先完成主仓库中正在进行的 Git 操作")
	}
	return nil
}
