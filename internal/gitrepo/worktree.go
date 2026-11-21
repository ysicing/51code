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
	"slices"
	"strings"
	"sync"
	"time"
)

// Worktree 的 Branch 和 ContainedIn 使用 refs/heads/ 全名；Head 为空表示尚无提交。
// ContainedIn 只包含通过祖先关系确认的其他本地分支，CleanupCandidate 还要求可安全移除。
type Worktree struct {
	Path               string   `json:"path"`
	Head               string   `json:"head"`
	Branch             string   `json:"branch"`
	Main               bool     `json:"main"`
	Current            bool     `json:"current"`
	Detached           bool     `json:"detached"`
	Bare               bool     `json:"bare"`
	Locked             bool     `json:"locked"`
	Prunable           bool     `json:"prunable"`
	LockReason         string   `json:"lockReason"`
	PruneReason        string   `json:"pruneReason"`
	Dirty              bool     `json:"dirty"`
	Ignored            bool     `json:"ignored"`
	Operation          string   `json:"operation"`
	Problem            string   `json:"problem"`
	ContainedIn        []string `json:"containedIn,omitempty"`
	CleanupCandidate   bool     `json:"cleanupCandidate"`
	ContainmentProblem string   `json:"containmentProblem,omitempty"`
}

// WorktreeReview 的目标始终是当前 Repository.Path。
type WorktreeReview struct {
	Worktree      Worktree `json:"worktree"`
	TargetHead    string   `json:"targetHead"`
	TargetBranch  string   `json:"targetBranch"`
	Token         string   `json:"token"`
	CanMerge      bool     `json:"canMerge"`
	CanRemove     bool     `json:"canRemove"`
	MergeProblem  string   `json:"mergeProblem"`
	RemoveProblem string   `json:"removeProblem"`
}

type worktreeState struct {
	Worktree Worktree
	// Status 是完整 status 输出的 SHA-256，只用于复核凭据，不保留可能很大的原文。
	Status         string
	OperationToken string
	GitDir         string
	GitDirStamp    string
	CommonStamp    string
	GitFile        string
	GitFileStamp   string
	RootStamp      string
	rootInfo       os.FileInfo
	gitFileInfo    os.FileInfo
	gitDirInfo     os.FileInfo
	commonInfo     os.FileInfo
}

type worktreeReviewState struct {
	source worktreeState
	target worktreeState
}

type worktreeGate struct {
	ready chan struct{}
	users int
}

var worktreeGates = struct {
	sync.Mutex
	entries map[string]*worktreeGate
}{entries: make(map[string]*worktreeGate)}

// 只串行化本进程的 worktree API；外部 Git 仍可能在最后一次核对后修改状态。
func lockWorktreeCommon(ctx context.Context, common string) (func(), error) {
	info, err := os.Stat(common)
	if err != nil {
		return nil, err
	}
	identity, err := worktreeFileIdentity(common, info)
	if err != nil {
		return nil, err
	}
	common = identity
	worktreeGates.Lock()
	gate := worktreeGates.entries[common]
	if gate == nil {
		gate = &worktreeGate{ready: make(chan struct{}, 1)}
		gate.ready <- struct{}{}
		worktreeGates.entries[common] = gate
	}
	gate.users++
	worktreeGates.Unlock()
	drop := func() {
		worktreeGates.Lock()
		gate.users--
		if gate.users == 0 {
			delete(worktreeGates.entries, common)
		}
		worktreeGates.Unlock()
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-gate.ready:
		if err := ctx.Err(); err != nil {
			gate.ready <- struct{}{}
			drop()
			return nil, err
		}
		return func() {
			gate.ready <- struct{}{}
			drop()
		}, nil
	}
}

func (repo *Repository) lockWorktree(ctx context.Context) (string, func(), error) {
	if err := repo.lockHistory(ctx); err != nil {
		return "", nil, err
	}
	common, err := worktreeCommonDir(ctx, repo.Path)
	if err != nil {
		repo.mu.Unlock()
		return "", nil, err
	}
	release, err := lockWorktreeCommon(ctx, common)
	if err != nil {
		repo.mu.Unlock()
		return "", nil, err
	}
	return common, func() {
		release()
		repo.mu.Unlock()
	}, nil
}

// Worktrees 保留缺失、锁定及可清理条目，单项故障由 Problem 表达。
func (repo *Repository) Worktrees(ctx context.Context) ([]Worktree, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	common, unlock, err := repo.lockWorktree(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	states, err := repo.worktrees(ctx, common)
	if err != nil {
		return nil, err
	}
	if err := repo.worktreeCleanupCandidates(ctx, states); err != nil {
		return nil, err
	}
	result := make([]Worktree, 0, len(states))
	for _, state := range states {
		result = append(result, state.Worktree)
	}
	return result, nil
}

func (repo *Repository) BranchWorktree(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	common, unlock, err := repo.lockWorktree(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	states, err := repo.worktreeEntries(ctx)
	if err != nil {
		return "", err
	}
	for _, state := range states {
		item := state.Worktree
		if item.Current || item.Branch != "refs/heads/"+name {
			continue
		}
		if item.Problem != "" {
			return "", errors.New(item.Problem)
		}
		if item.Prunable || item.Bare {
			return "", fmt.Errorf("分支所在工作树不可用：%s", item.Path)
		}
		target := &Repository{Path: item.Path}
		paths, err := target.revParsePaths(ctx, "--git-common-dir", "--show-toplevel")
		if err != nil {
			return "", err
		}
		if !worktreeSamePath(paths[0], common) || !worktreeSamePath(paths[1], item.Path) {
			return "", fmt.Errorf("工作树已变化，请刷新后重试：%s", item.Path)
		}
		return item.Path, nil
	}
	return "", nil
}

// ReviewWorktree 仅检查列表中的条目，不把任意路径当成链接工作树。
func (repo *Repository) ReviewWorktree(ctx context.Context, path string) (WorktreeReview, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	common, unlock, err := repo.lockWorktree(ctx)
	if err != nil {
		return WorktreeReview{}, err
	}
	defer unlock()
	review, _, err := repo.reviewWorktree(ctx, common, path)
	return review, err
}

func (repo *Repository) reviewWorktree(ctx context.Context, common, path string) (WorktreeReview, worktreeReviewState, error) {
	canonical, err := worktreePath(path)
	if err != nil {
		return WorktreeReview{}, worktreeReviewState{}, err
	}
	// 审查只需要源和目标（当前）工作树；其余条目不参与凭据，逐个完整检查只会拖慢持锁时间。
	states, err := repo.worktreeEntries(ctx)
	if err != nil {
		return WorktreeReview{}, worktreeReviewState{}, err
	}
	if err := inspectWorktrees(ctx, common, states, func(worktree Worktree) bool {
		return worktree.Current || worktreeSamePath(worktree.Path, canonical)
	}); err != nil {
		return WorktreeReview{}, worktreeReviewState{}, err
	}
	var source, target *worktreeState
	for index := range states {
		state := &states[index]
		if worktreeSamePath(state.Worktree.Path, canonical) {
			source = state
		}
		if state.Worktree.Current {
			target = state
		}
	}
	if source == nil {
		return WorktreeReview{}, worktreeReviewState{}, errors.New("路径不在当前仓库的工作树列表中")
	}
	if target == nil {
		return WorktreeReview{}, worktreeReviewState{}, errors.New("当前目标不在仓库工作树列表中，请重新打开仓库")
	}
	review := WorktreeReview{
		Worktree: source.Worktree, TargetHead: target.Worktree.Head, TargetBranch: target.Worktree.Branch,
		MergeProblem:  worktreeMergeProblem(source.Worktree, target.Worktree),
		RemoveProblem: worktreeRemoveProblem(source.Worktree, target.Worktree),
	}
	review.CanMerge = review.MergeProblem == ""
	review.CanRemove = review.RemoveProblem == ""
	data, err := json.Marshal(struct {
		Common string
		Source worktreeState
		Target worktreeState
	}{common, *source, *target})
	if err != nil {
		return WorktreeReview{}, worktreeReviewState{}, err
	}
	sum := sha256.Sum256(data)
	review.Token = hex.EncodeToString(sum[:])
	return review, worktreeReviewState{source: *source, target: *target}, nil
}

func worktreeUnavailable(worktree Worktree) string {
	switch {
	case worktree.Locked:
		return "工作树已锁定，请先解锁"
	case worktree.Prunable:
		return "工作树已失效并标记为可清理，请先修复"
	case worktree.Bare:
		return "裸仓库没有可操作的工作树"
	case worktree.Problem != "":
		return worktree.Problem
	}
	return ""
}

func worktreeMergeProblem(source, target Worktree) string {
	switch {
	case source.Current:
		return "不能将当前工作树合并到自身"
	}
	if problem := worktreeUnavailable(source); problem != "" {
		return problem
	}
	if problem := worktreeUnavailable(target); problem != "" {
		return "目标工作树：" + problem
	}
	switch {
	case source.Head == "":
		return "源工作树尚无提交"
	case source.Operation != "":
		return "请先完成源工作树正在进行的 Git 操作"
	case source.Dirty:
		return "请先提交或贮藏源工作树的未提交及未跟踪文件"
	case target.Detached || !strings.HasPrefix(target.Branch, "refs/heads/"):
		return "目标工作树处于分离 HEAD，请先切换到本地分支"
	case target.Operation != "":
		return "请先完成目标工作树正在进行的 Git 操作"
	case target.Dirty:
		return "请先提交或贮藏目标工作树的未提交及未跟踪文件"
	}
	return ""
}

func worktreeRemoveProblem(source, target Worktree) string {
	switch {
	case source.Main:
		return "不能移除主工作树"
	case source.Current:
		return "不能移除当前工作树"
	}
	if problem := worktreeUnavailable(source); problem != "" {
		return problem
	}
	switch {
	case source.Dirty:
		return "工作树含有未提交或未跟踪文件，不能移除"
	case source.Operation != "":
		return "工作树正在进行 Git 操作，不能移除"
	case target.Problem != "":
		return "无法核实目标工作树：" + target.Problem
	}
	return ""
}

// CreateWorktree 不覆盖路径，不强制检出；失败后不删除分支或用户目录。
func (repo *Repository) CreateWorktree(ctx context.Context, path, branch, start string, existing bool) (Worktree, error) {
	// 检出新工作树可能经 LFS smudge 下载大量对象，与切换分支使用同一上限。
	ctx, cancel := context.WithTimeout(ctx, checkoutTimeout)
	defer cancel()
	common, unlock, err := repo.lockWorktree(ctx)
	if err != nil {
		return Worktree{}, err
	}
	defer unlock()
	// 创建前只需各工作树的路径与分支，用于重叠和占用检查，不读取各自的文件状态。
	states, err := repo.worktreeEntries(ctx)
	if err != nil {
		return Worktree{}, err
	}
	path, err = worktreeCreatePath(path, common, states)
	if err != nil {
		return Worktree{}, err
	}
	if strings.HasPrefix(branch, "refs/") && !strings.HasPrefix(branch, "refs/heads/") {
		return Worktree{}, errors.New("工作树只能使用本地分支")
	}
	branch = strings.TrimPrefix(branch, "refs/heads/")
	if err := repo.validateBranch(ctx, branch); err != nil {
		if ctx.Err() != nil {
			return Worktree{}, ctx.Err()
		}
		return Worktree{}, err
	}
	ref := "refs/heads/" + branch
	for _, state := range states {
		if state.Worktree.Branch == ref {
			return Worktree{}, errors.New("分支已被其他工作树占用")
		}
	}
	_, refErr := repo.git(ctx, "show-ref", "--verify", "--quiet", ref)
	if refErr != nil && !exitCode(refErr, 1) {
		return Worktree{}, refErr
	}
	if existing && refErr != nil {
		return Worktree{}, errors.New("本地分支不存在")
	}
	if !existing && refErr == nil {
		return Worktree{}, errors.New("本地分支已存在，请选择使用已有分支")
	}
	if existing {
		start = ref
	} else if start == "" {
		start = "HEAD"
	}
	head, err := repo.comparisonCommit(ctx, start)
	if err != nil {
		return Worktree{}, err
	}
	if checked, err := worktreeCreatePath(path, common, states); err != nil || checked != path {
		if err != nil {
			return Worktree{}, err
		}
		return Worktree{}, errors.New("工作树目标路径在检查后已改变")
	}
	args := []string{"worktree", "add"}
	if existing {
		args = append(args, "--", path, branch)
	} else {
		args = append(args, "--no-track", "-b", branch, "--", path, head)
	}
	if _, err := run(ctx, repo.Path, "", checkoutTimeout, args...); err != nil {
		return Worktree{}, err
	}
	states, err = repo.worktreeEntries(ctx)
	if err != nil {
		return Worktree{}, err
	}
	if err := inspectWorktrees(ctx, common, states, func(worktree Worktree) bool {
		return worktreeSamePath(worktree.Path, path)
	}); err != nil {
		return Worktree{}, err
	}
	for _, state := range states {
		if worktreeSamePath(state.Worktree.Path, path) {
			if state.Worktree.Problem != "" || state.Worktree.Head != head || state.Worktree.Branch != ref {
				return state.Worktree, errors.New("工作树已创建，但状态发生变化，请刷新核实")
			}
			return state.Worktree, nil
		}
	}
	return Worktree{}, fmt.Errorf("工作树创建后未出现在列表中，请刷新核实：%s", path)
}

// MergeWorktree 合并复核后的固定提交；冲突状态留给现有操作界面处理。
func (repo *Repository) MergeWorktree(ctx context.Context, path, token string) error {
	// 合并会改写目标工作区，被提前杀掉会留下 index.lock 和半完成的合并。
	ctx, cancel := context.WithTimeout(ctx, checkoutTimeout)
	defer cancel()
	common, unlock, err := repo.lockWorktree(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	review, state, err := repo.reviewWorktree(ctx, common, path)
	if err != nil {
		return err
	}
	if token == "" || token != review.Token {
		return errors.New("工作树状态已改变，请刷新后重新确认")
	}
	if !review.CanMerge {
		return errors.New(review.MergeProblem)
	}
	if err := state.source.checkIdentity(ctx, common); err != nil {
		return err
	}
	if err := state.target.checkIdentity(ctx, common); err != nil {
		return err
	}
	// 非快进合并时 --no-overwrite-ignore 不能阻止覆盖目标工作树中被忽略的文件。
	if err := repo.checkMergeIgnoredPaths(ctx, review.Worktree.Head); err != nil {
		return err
	}
	_, err = run(ctx, repo.Path, "", checkoutTimeout, "merge", "--no-edit", "--no-autostash",
		"--no-overwrite-ignore", "--", review.Worktree.Head)
	return err
}

// RemoveWorktree 不强制移除，不删除分支，也不清理其他工作树元数据。
func (repo *Repository) RemoveWorktree(ctx context.Context, path, token string) error {
	return repo.removeWorktree(ctx, path, token, false)
}

func (repo *Repository) removeWorktree(ctx context.Context, path, token string, cleanup bool) error {
	// 删除大型工作树目录可能耗时较长，中途被杀会留下部分删除的目录。
	ctx, cancel := context.WithTimeout(ctx, checkoutTimeout)
	defer cancel()
	common, unlock, err := repo.lockWorktree(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	reviewWorktree := repo.reviewWorktree
	if cleanup {
		reviewWorktree = repo.reviewWorktreeCleanup
	}
	review, state, err := reviewWorktree(ctx, common, path)
	if err != nil {
		return err
	}
	if token == "" || token != review.Token {
		return errors.New("工作树状态已改变，请刷新后重新确认")
	}
	if !review.CanRemove {
		return errors.New(review.RemoveProblem)
	}
	if err := state.target.checkIdentity(ctx, common); err != nil {
		return err
	}
	if err := state.source.checkIdentity(ctx, common); err != nil {
		return err
	}
	if cleanup {
		head, err := (&Repository{Path: review.Worktree.Path}).head(ctx)
		if err != nil {
			return err
		}
		if head != review.Worktree.Head {
			return errors.New("工作树状态已改变，请刷新后重新确认")
		}
		branches, err := repo.worktreeContainingBranches(ctx, review.Worktree.Head)
		if err != nil {
			return err
		}
		branches = worktreeOtherBranches(branches, review.Worktree.Branch)
		if !slices.Equal(branches, review.Worktree.ContainedIn) {
			return errors.New("工作树的提交包含关系已改变，请刷新后重新确认")
		}
	}
	_, err = run(ctx, repo.Path, "", checkoutTimeout, "worktree", "remove", "--", review.Worktree.Path)
	return err
}
