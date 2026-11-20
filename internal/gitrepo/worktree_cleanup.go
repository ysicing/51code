// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// worktreeContainingBranches 一次查询全部包含该提交的本地分支，不按分支启动进程。
// 排除符号引用，避免源分支的别名被误认为独立的保留目标；不推断 squash 或补丁等价。
func (repo *Repository) worktreeContainingBranches(ctx context.Context, head string) ([]string, error) {
	if head == "" {
		return nil, nil
	}
	if err := validCommitHash(head); err != nil {
		return nil, err
	}
	output, err := repo.git(ctx, "--no-replace-objects", "for-each-ref", "--contains="+head,
		"--sort=refname", "--format=%(refname)%00%(symref)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if line == "" {
			continue
		}
		ref, symbolic, found := strings.Cut(line, "\x00")
		if !found || !strings.HasPrefix(ref, "refs/heads/") || ref == "refs/heads/" {
			return nil, errors.New("无法解析包含工作树提交的本地分支")
		}
		if symbolic == "" {
			branches = append(branches, ref)
		}
	}
	return branches, nil
}

func worktreeOtherBranches(branches []string, own string) []string {
	var others []string
	for _, branch := range branches {
		if branch != own {
			others = append(others, branch)
		}
	}
	return others
}

func (repo *Repository) worktreeCleanupCandidates(ctx context.Context, states []worktreeState) error {
	var current *Worktree
	for index := range states {
		if states[index].Worktree.Current {
			current = &states[index].Worktree
			break
		}
	}
	type containment struct {
		branches []string
		err      error
	}
	// 同一 HEAD 的工作树共用一次祖先查询；失败只影响候选判断，不能封锁普通移除入口。
	cache := make(map[string]containment)
	for index := range states {
		item := &states[index].Worktree
		if item.Head == "" || item.Bare || item.Problem != "" {
			continue
		}
		result, found := cache[item.Head]
		if !found {
			result.branches, result.err = repo.worktreeContainingBranches(ctx, item.Head)
			cache[item.Head] = result
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if result.err != nil {
			item.ContainmentProblem = "无法确认工作树提交的包含关系：" + result.err.Error()
			continue
		}
		item.ContainedIn = worktreeOtherBranches(result.branches, item.Branch)
		item.CleanupCandidate = current != nil && len(item.ContainedIn) > 0 &&
			worktreeRemoveProblem(*item, *current) == ""
	}
	return nil
}

// ReviewWorktreeCleanup 在打开清理确认时重新确认祖先关系，普通移除不依赖该检查。
func (repo *Repository) ReviewWorktreeCleanup(ctx context.Context, path string) (WorktreeReview, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	common, unlock, err := repo.lockWorktree(ctx)
	if err != nil {
		return WorktreeReview{}, err
	}
	defer unlock()
	review, _, err := repo.reviewWorktreeCleanup(ctx, common, path)
	return review, err
}

func (repo *Repository) reviewWorktreeCleanup(ctx context.Context, common, path string) (WorktreeReview, worktreeReviewState, error) {
	review, state, err := repo.reviewWorktree(ctx, common, path)
	if err != nil {
		return review, state, err
	}
	branches, err := repo.worktreeContainingBranches(ctx, review.Worktree.Head)
	if err != nil {
		return review, state, err
	}
	review.Worktree.ContainedIn = worktreeOtherBranches(branches, review.Worktree.Branch)
	if len(review.Worktree.ContainedIn) == 0 && review.CanRemove {
		review.CanRemove = false
		review.RemoveProblem = "没有可通过提交祖先关系确认包含此 HEAD 的其他本地分支，请刷新或使用普通移除"
	}
	review.Worktree.CleanupCandidate = review.CanRemove
	// 清理凭据绑定确认中展示的分支名称，并与普通移除的凭据分离。
	data, err := json.Marshal(struct {
		RemovalToken string
		ContainedIn  []string
	}{review.Token, review.Worktree.ContainedIn})
	if err != nil {
		return review, state, err
	}
	sum := sha256.Sum256(data)
	review.Token = hex.EncodeToString(sum[:])
	return review, state, nil
}

// RemoveWorktreeCleanup 只移除复核过的单个候选目录，保留所有分支。
func (repo *Repository) RemoveWorktreeCleanup(ctx context.Context, path, token string) error {
	return repo.removeWorktree(ctx, path, token, true)
}
