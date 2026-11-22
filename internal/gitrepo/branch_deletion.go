// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type BranchDeletionReview struct {
	Name      string
	Head      string
	Worktree  *Worktree
	CanDelete bool
	Unmerged  bool
	Problem   string
	Token     string
}

type branchDeletionState struct {
	Common        string
	Name          string
	Force         bool
	Head          string
	Reference     string
	ReferenceHead string
	Sources       []worktreeState
	Target        worktreeState
}

type BranchDeletionPartialError struct {
	Branch string
	Path   string
	Head   string
	Err    error
}

func (err *BranchDeletionPartialError) Error() string {
	return fmt.Sprintf("工作树已移除，但分支 %s 的删除流程未完成。确认时的提交：%s。请刷新核实分支状态后重试。\n%s\n%v",
		err.Branch, err.Head, err.Path, err.Err)
}

func (err *BranchDeletionPartialError) Unwrap() error { return err.Err }

func (repo *Repository) ReviewBranchDeletion(ctx context.Context, name string, force bool) (BranchDeletionReview, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	common, unlock, err := repo.lockWorktree(ctx)
	if err != nil {
		return BranchDeletionReview{}, err
	}
	defer unlock()
	review, _, err := repo.reviewBranchDeletion(ctx, common, name, force)
	return review, err
}

func (repo *Repository) reviewBranchDeletion(ctx context.Context, common, name string, force bool) (BranchDeletionReview, branchDeletionState, error) {
	review := BranchDeletionReview{Name: name}
	state := branchDeletionState{Common: common, Name: name, Force: force}
	if err := repo.validateBranch(ctx, name); err != nil {
		return review, state, err
	}
	ref := "refs/heads/" + name
	output, err := repo.git(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return review, state, err
	}
	state.Head = strings.TrimSpace(output)
	review.Head = state.Head
	// 只完整检查当前工作树和持有该分支的工作树；删除普通分支时通常只有当前工作树一项。
	worktrees, err := repo.worktreeEntries(ctx)
	if err != nil {
		return review, state, err
	}
	if err := inspectWorktrees(ctx, common, worktrees, func(worktree Worktree) bool {
		return worktree.Current || worktree.Branch == ref
	}); err != nil {
		return review, state, err
	}
	foundTarget := false
	for _, worktree := range worktrees {
		if worktree.Worktree.Current {
			state.Target, foundTarget = worktree, true
		}
		if worktree.Worktree.Branch == ref {
			state.Sources = append(state.Sources, worktree)
		}
	}
	if !foundTarget {
		return review, state, errors.New("当前目标不在仓库工作树列表中，请重新打开仓库")
	}
	if len(state.Sources) > 0 {
		review.Worktree = &state.Sources[0].Worktree
		review.Problem = worktreeRemoveProblem(*review.Worktree, state.Target.Worktree)
		if review.Problem == "" && review.Worktree.Head != state.Head {
			return review, state, errors.New("分支在预检期间已改变，请重新确认")
		}
	}
	if len(state.Sources) > 1 {
		review.Problem = "分支被多个工作树使用，请先分别处理，不能批量移除"
	}
	if review.Problem == "" {
		occupied, err := branchUsedByWorktreeOperation(common, ref)
		if err != nil {
			return review, state, err
		}
		if occupied {
			review.Problem = "分支正被变基或二分查找使用，请先完成该操作"
		}
	}
	if !force {
		state.Reference, state.ReferenceHead, err = repo.branchDeletionReference(ctx, ref, state.Target.Worktree.Head)
		if err != nil {
			return review, state, err
		}
		if review.Problem == "" {
			if state.ReferenceHead == "" {
				review.Problem = "无法确认分支已合并，请选择“强制删除分支…”并再次确认"
			} else {
				_, err = repo.git(ctx, "merge-base", "--is-ancestor", state.Head, state.ReferenceHead)
				if exitCode(err, 1) {
					review.Unmerged = true
					review.Problem = "分支尚未合并到上游或当前 HEAD。"
				} else if err != nil {
					return review, state, err
				}
			}
		}
	}
	review.CanDelete = review.Problem == ""
	data, err := json.Marshal(state)
	if err != nil {
		return review, state, err
	}
	sum := sha256.Sum256(data)
	review.Token = hex.EncodeToString(sum[:])
	return review, state, nil
}

func (repo *Repository) branchDeletionReference(ctx context.Context, ref, head string) (string, string, error) {
	output, err := repo.git(ctx, "for-each-ref", "--format=%(upstream)", "--", ref)
	if err != nil {
		return "", "", err
	}
	upstream := strings.TrimSpace(output)
	if upstream != "" {
		hash, err := repo.git(ctx, "rev-parse", "--verify", "--quiet", upstream+"^{commit}")
		if err == nil {
			return upstream, strings.TrimSpace(hash), nil
		}
		if !exitCode(err, 1) {
			return "", "", err
		}
	}
	return "HEAD", head, nil
}

func (repo *Repository) DeleteReviewedBranch(ctx context.Context, name string, force bool, token string) error {
	// 可能先移除持有分支的工作树，大目录删除不能被两分钟上限中途打断。
	ctx, cancel := context.WithTimeout(ctx, checkoutTimeout)
	defer cancel()
	common, unlock, err := repo.lockWorktree(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	review, state, err := repo.reviewBranchDeletion(ctx, common, name, force)
	if err != nil {
		return err
	}
	if token == "" || token != review.Token {
		return errors.New("分支或工作树状态已改变，请刷新后重新确认")
	}
	if !review.CanDelete {
		return errors.New(review.Problem)
	}
	if err := state.Target.checkIdentity(ctx, common); err != nil {
		return err
	}
	removed := review.Worktree != nil
	partial := func(err error) error {
		return &BranchDeletionPartialError{Branch: name, Path: review.Worktree.Path, Head: review.Head, Err: err}
	}
	if removed {
		if err := state.Sources[0].checkIdentity(ctx, common); err != nil {
			return err
		}
		if _, err := run(ctx, repo.Path, "", checkoutTimeout, "worktree", "remove", "--", review.Worktree.Path); err != nil {
			if _, statErr := os.Lstat(review.Worktree.Path); os.IsNotExist(statErr) {
				return partial(err)
			}
			return err
		}
		// 两步不能组成同一 Git 事务；目录移除后仍须复核引用，失败时保留分支并报告部分完成。
		after, next, err := repo.reviewBranchDeletion(ctx, common, name, force)
		if err != nil {
			return partial(err)
		}
		if err := state.Target.checkIdentity(ctx, common); err != nil {
			return partial(err)
		}
		if !after.CanDelete || after.Worktree != nil || next.Head != state.Head ||
			next.Reference != state.Reference || next.ReferenceHead != state.ReferenceHead ||
			next.Target.Worktree.Head != state.Target.Worktree.Head ||
			next.Target.Worktree.Branch != state.Target.Worktree.Branch {
			return partial(errors.New("移除工作树后分支状态已改变，已停止删除分支"))
		}
	}
	err = repo.deleteBranchAt(ctx, state)
	if err != nil && removed {
		return partial(err)
	}
	return err
}

func (repo *Repository) deleteBranchAt(ctx context.Context, state branchDeletionState) error {
	ref := "refs/heads/" + state.Name
	occupied, err := repo.git(ctx, "for-each-ref", "--format=%(worktreepath)", "--", ref)
	if err != nil {
		return err
	}
	if strings.TrimSpace(occupied) != "" {
		return errors.New("分支已被工作树使用，请刷新后重新确认")
	}
	inOperation, err := branchUsedByWorktreeOperation(state.Common, ref)
	if err != nil {
		return err
	}
	if inOperation {
		return errors.New("分支正被变基或二分查找使用，请先完成该操作")
	}
	if !state.Force {
		head, err := repo.git(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
		if err != nil && !exitCode(err, 1) {
			return err
		}
		reference, tip, err := repo.branchDeletionReference(ctx, ref, strings.TrimSpace(head))
		if err != nil {
			return err
		}
		if reference != state.Reference || tip != state.ReferenceHead {
			return errors.New("用于确认合并的引用已改变，请刷新后重新确认")
		}
	}
	if _, err := repo.git(ctx, "update-ref", "--no-deref", "-d", ref, state.Head); err != nil {
		return fmt.Errorf("无法删除确认时的分支引用，请刷新后重新确认：%w", err)
	}
	section := "branch." + state.Name
	_, err = repo.git(ctx, "config", "--local", "--remove-section", section)
	if err != nil && !(exitCode(err, 128) && err.Error() == "fatal: no such section: "+section) {
		return fmt.Errorf("分支已删除，但无法清理分支配置：%w", err)
	}
	return nil
}

// 分支在变基或二分查找期间可能处于分离 HEAD，单查工作树当前分支会漏掉。
func branchUsedByWorktreeOperation(common, ref string) (bool, error) {
	directory := filepath.Join(common, "worktrees")
	entries, err := os.ReadDir(directory)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	gitDirs := []string{common}
	for _, entry := range entries {
		if entry.IsDir() {
			gitDirs = append(gitDirs, filepath.Join(directory, entry.Name()))
		}
	}
	for _, gitDir := range gitDirs {
		for _, marker := range []string{"rebase-merge/head-name", "rebase-apply/head-name", "rebase-merge/update-refs", "BISECT_START"} {
			data, err := os.ReadFile(filepath.Join(gitDir, marker))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return false, err
			}
			for line := range strings.SplitSeq(string(data), "\n") {
				if line == ref || (marker == "BISECT_START" && "refs/heads/"+line == ref) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
