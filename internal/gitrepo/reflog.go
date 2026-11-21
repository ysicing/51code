// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

const ReflogRecoveryLimit = 1000

type LostCommit struct {
	Hash, Subject, Ref, Date, Action string
}

type LostCommits struct {
	Commits []LostCommit
	Limited bool
}

// LostCommits 查找当前工作树 HEAD 和本地分支的 reflog 中不再被引用的提交。
func (repo *Repository) LostCommits(ctx context.Context) (LostCommits, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := repo.lockHistory(ctx); err != nil {
		return LostCommits{}, err
	}
	defer repo.mu.Unlock()
	head, err := repo.head(ctx)
	if err != nil {
		return LostCommits{}, err
	}
	args := []string{"log", "-g", "--branches", "-z", "--no-show-signature", "--no-notes",
		"--date=iso-strict", "--format=%H%x00%gD%x00%<(240,trunc)%gs%x00%<(240,trunc)%s",
		"--max-count=" + strconv.Itoa(ReflogRecoveryLimit+1)}
	if head != "" {
		args = append(args, "HEAD")
	}
	var result LostCommits
	var entries []LostCommit
	seen := make(map[string]bool)
	var input strings.Builder
	var fields [4]string
	count, field := 0, 0
	err = runRecords(ctx, repo.Path, 30*time.Second, func(value string) error {
		fields[field] = value
		field++
		if field != len(fields) {
			return nil
		}
		field = 0
		count++
		if count > ReflogRecoveryLimit {
			result.Limited = true
			return nil
		}
		hash, selector := fields[0], fields[1]
		ref, date, ok := strings.Cut(selector, "@{")
		if !ok || !strings.HasSuffix(date, "}") || validCommitHash(hash) != nil {
			return errors.New("无法解析 Git reflog 记录")
		}
		if !seen[hash] {
			seen[hash] = true
			entries = append(entries, LostCommit{
				Hash: hash, Ref: ref, Date: strings.TrimSuffix(date, "}"),
				Action: strings.TrimRight(fields[2], " "), Subject: strings.TrimRight(fields[3], " "),
			})
			input.WriteString(hash + "\n")
		}
		return nil
	}, append(args, "--")...)
	if err != nil {
		return LostCommits{}, err
	}
	if field != 0 {
		return LostCommits{}, errors.New("Git reflog 记录不完整")
	}
	if len(entries) == 0 {
		return result, nil
	}

	// 一次可达性遍历过滤所有候选，已由分支、标签、贮藏或工作树保留的提交无需恢复。
	unreachable := make(map[string]bool)
	writer := nulRecordWriter{record: func(hash string) error {
		if seen[hash] {
			unreachable[hash] = true
		}
		return nil
	}}
	if err := runTo(ctx, repo.Path, input.String(), 30*time.Second, &writer, func() string { return "" },
		"log", "--stdin", "--not", "--all", "-z", "--no-show-signature", "--no-notes", "--format=%H", "--"); err != nil {
		return LostCommits{}, err
	}
	if err := writer.flush(); err != nil {
		return LostCommits{}, err
	}
	for _, entry := range entries {
		if unreachable[entry.Hash] {
			result.Commits = append(result.Commits, entry)
		}
	}
	return result, nil
}
