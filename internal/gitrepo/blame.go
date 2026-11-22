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

type LineBlame struct {
	Hash       string
	Author     string
	Subject    string
	AuthoredAt time.Time
}

func (repo *Repository) BlameLine(ctx context.Context, content *FileContent, line int) (LineBlame, error) {
	if content == nil || !content.Present || content.Binary || content.Truncated || content.Unsupported || line < 1 {
		return LineBlame{}, errors.New("无法读取此行的提交归属")
	}
	if err := validBrowsePath(content.Path); err != nil {
		return LineBlame{}, err
	}
	var expected string
	found := false
	remaining := line
	for text := range strings.SplitSeq(strings.TrimSuffix(content.Text, "\n"), "\n") {
		remaining--
		if remaining == 0 {
			expected, found = text, true
			break
		}
	}
	if content.Text == "" || !found {
		return LineBlame{}, errors.New("行号不在文件中")
	}
	if content.blameBase == "" {
		return LineBlame{}, ctx.Err()
	}
	if err := validCommitHash(content.blameBase); err != nil {
		return LineBlame{}, err
	}
	number := strconv.Itoa(line)
	args := []string{"blame", "--line-porcelain", "--encoding=UTF-8", "--no-textconv", "-L", number + "," + number}
	var output string
	var err error
	if content.Revision == "WORKTREE" || content.Revision == "INDEX" {
		// 旧版 Git 不允许同时传提交和 --contents。使用已预览的原文，并核对隐式 HEAD。
		if err := repo.checkBlameHead(ctx, content.blameBase); err != nil {
			return LineBlame{}, err
		}
		output, err = run(ctx, repo.Path, content.Text, 10*time.Second,
			append(args, "--contents", "-", "--", content.Path)...)
		if headErr := repo.checkBlameHead(ctx, content.blameBase); headErr != nil {
			return LineBlame{}, headErr
		}
		if exitCode(err, 128) && strings.HasPrefix(err.Error(), "fatal: no such path ") {
			return LineBlame{}, nil
		}
	} else {
		output, err = run(ctx, repo.Path, "", 10*time.Second, append(args, content.blameBase, "--", content.Path)...)
	}
	if err != nil {
		return LineBlame{}, err
	}
	return parseLineBlame(output, expected)
}

func (repo *Repository) checkBlameHead(ctx context.Context, base string) error {
	head, err := repo.head(ctx)
	if err != nil {
		return err
	}
	if head != base {
		return errors.New("仓库 HEAD 已变化，请刷新文件预览")
	}
	return nil
}

func parseLineBlame(output, expected string) (LineBlame, error) {
	header, metadata, _ := strings.Cut(output, "\n")
	hash, _, _ := strings.Cut(header, " ")
	if err := validCommitHash(hash); err != nil {
		return LineBlame{}, err
	}
	result := LineBlame{Hash: hash}
	for entry := range strings.SplitSeq(metadata, "\n") {
		if text, ok := strings.CutPrefix(entry, "\t"); ok {
			if strings.TrimSuffix(text, "\r") != strings.TrimSuffix(expected, "\r") {
				break
			}
			if strings.Trim(hash, "0") == "" {
				return LineBlame{}, nil
			}
			return result, nil
		}
		key, value, _ := strings.Cut(entry, " ")
		switch key {
		case "author":
			result.Author = value
		case "summary":
			result.Subject = value
		case "author-time":
			if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
				result.AuthoredAt = time.Unix(seconds, 0)
			}
		}
	}
	return LineBlame{}, errors.New("行归属与文件预览不一致")
}
