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
	"strings"
	"time"
)

type BranchDeletionReview struct {
	Name      string
	Head      string
	Worktree  *Worktree
	CanDelete bool
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
	worktrees, err := repo.worktrees(ctx, common)
	if err != nil {
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
					review.Problem = "分支尚未合并到上游或当前 HEAD，请选择“强制删除分支…”并再次确认"
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
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
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
		if _, err := run(ctx, repo.Path, "", 2*time.Minute, "worktree", "remove", "--", review.Worktree.Path); err != nil {
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
	option := "--delete"
	if force {
		option = "-D"
	}
	_, err = repo.git(ctx, "branch", option, "--", name)
	if err != nil && removed {
		return partial(err)
	}
	return err
}
