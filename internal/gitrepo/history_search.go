// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type CommitFile struct {
	Path         string `json:"path"`
	Status       string `json:"status"`
	OriginalPath string `json:"originalPath"`
}

type HistoryFilter struct {
	CurrentBranch bool
	HideRemotes   bool
	Mode          string
	Since         string
	Until         string
}

const (
	DefaultHistoryPageSize = 100
	MaxHistoryPageSize     = 500
	// MaxHistoryReloadSize 用于刷新时一次性重读已加载的历史；按页 --skip 重读
	// 会让 git 反复遍历前面的提交，总代价随已加载数量平方增长。
	MaxHistoryReloadSize = 50000
)

func (filter HistoryFilter) validate() error {
	switch filter.Mode {
	case "", "message", "hash", "branch", "changes", "author":
	default:
		return errors.New("搜索类型无效")
	}
	for _, date := range []string{filter.Since, filter.Until} {
		if date != "" {
			if _, err := time.Parse(time.DateOnly, date); err != nil {
				return errors.New("日期格式应为 YYYY-MM-DD")
			}
		}
	}
	if filter.Since != "" && filter.Until != "" && filter.Since > filter.Until {
		return errors.New("开始日期不能晚于结束日期")
	}
	return nil
}

func (repo *Repository) SearchHistory(ctx context.Context, query string, path string, skip int, filter HistoryFilter, pageSize ...int) ([]Commit, error) {
	limit := DefaultHistoryPageSize
	if len(pageSize) > 1 {
		return nil, errors.New("历史分页无效")
	}
	if len(pageSize) == 1 {
		limit = pageSize[0]
	}
	if limit < 1 || limit > MaxHistoryReloadSize {
		return nil, errors.New("历史分页无效")
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	if skip < 0 || skip > 1<<31-1 {
		return nil, errors.New("历史分页无效")
	}
	if strings.ContainsRune(query, 0) {
		return nil, errors.New("搜索内容无效")
	}
	if path != "" {
		if err := validPath(path); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := repo.lockHistory(ctx); err != nil {
		return nil, err
	}
	defer repo.mu.Unlock()
	if filter.CurrentBranch {
		head, err := repo.head(ctx)
		if err != nil {
			return nil, err
		}
		if head == "" {
			return []Commit{}, nil
		}
	}
	return repo.streamHistory(ctx, query, path, skip, limit, filter)
}

func validCommitHash(hash string) error {
	if len(hash) != 40 && len(hash) != 64 {
		return errors.New("提交 ID 无效")
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return errors.New("提交 ID 无效")
	}
	return nil
}

func (repo *Repository) lockHistory(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if repo.mu.TryLock() {
		return nil
	}
	// 等待共享仓库锁也必须受 Context 限制，不能等到获取锁后才响应取消。
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := ctx.Err(); err != nil {
				return err
			}
			if repo.mu.TryLock() {
				return nil
			}
		}
	}
}

// CommitFiles 对首提交使用空树，对合并提交只比较第一父提交。
func (repo *Repository) CommitFiles(ctx context.Context, hash string) ([]CommitFile, error) {
	if err := validCommitHash(hash); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := repo.lockHistory(ctx); err != nil {
		return nil, err
	}
	defer repo.mu.Unlock()
	files, parents, err := repo.commitFiles(ctx, hash)
	if err != nil {
		return nil, err
	}
	repo.rememberCommitChanges(commitChangesKey(hash), files, parents)
	return files, nil
}

// commitFiles 用一次 git log 同时取得提交校验、父提交和文件列表，代替 cat-file -t、
// diff-tree 与 rev-list 三个进程。parents 与 rev-list --parents 相同，首项是提交自身。
//
// 与 diff-tree 等价的约束：标签对象会被 log 剥离为所指提交，tree/blob 输入输出为空且退出码为 0，
// 因此必须核对输出的 %H 与输入一致（与原先 cat-file -t 只接受 commit 的语义相同）；
// 入口已由 validCommitHash 限定为完整十六进制 ID，不会出现缩写或引用名。
// log 属于 porcelain，显式固定签名、装饰、颜色、注释、改名检测、子模块、相对路径与输出顺序，
// 避免 log.showSignature、log.decorate、diff.renames、color.* 等用户配置改变结果。
func (repo *Repository) commitFiles(ctx context.Context, hash string) ([]CommitFile, []string, error) {
	output, err := repo.git(ctx, "-c", "diff.orderFile="+os.DevNull, "log", "--no-walk", "--no-show-signature",
		"--no-decorate", "--no-color", "--no-notes", "--format=%H%x00%P", "--root", "--diff-merges=first-parent", "--name-status", "-r", "-z",
		"--no-ext-diff", "--no-textconv", "--find-renames", "--no-relative", "--ignore-submodules=none", hash, "--")
	if err != nil {
		return nil, nil, err
	}
	commit, rest, ok := strings.Cut(output, "\x00")
	if !ok || !strings.EqualFold(commit, hash) {
		return nil, nil, errors.New("提交 ID 未指向提交对象")
	}
	parents, rest, ok := strings.Cut(rest, "\x00")
	if !ok {
		return nil, nil, errors.New("无法解析提交文件")
	}
	// 有文件变更时，提交信息与文件列表之间以一个换行分隔；空提交没有后续内容。
	if rest != "" {
		if rest[0] != '\n' {
			return nil, nil, errors.New("无法解析提交文件")
		}
		rest = rest[1:]
	}
	files, err := parseCommitFiles(rest)
	if err != nil {
		return nil, nil, err
	}
	return files, append([]string{commit}, strings.Fields(parents)...), nil
}

func parseCommitFiles(output string) ([]CommitFile, error) {
	files := []CommitFile{}
	if output == "" {
		return files, nil
	}
	if !strings.HasSuffix(output, "\x00") {
		return nil, errors.New("无法解析提交文件")
	}
	fields := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	for index := 0; index < len(fields); {
		status := fields[index]
		index++
		if status == "" || !strings.ContainsRune("ACDMRTUXB", rune(status[0])) || index >= len(fields) {
			return nil, errors.New("无法解析提交文件")
		}
		file := CommitFile{Status: status[:1], Path: fields[index]}
		index++
		if file.Status == "R" || file.Status == "C" {
			file.OriginalPath = file.Path
			if index >= len(fields) {
				return nil, errors.New("Git 重命名记录不完整")
			}
			file.Path = fields[index]
			index++
			if err := validPath(file.OriginalPath); err != nil {
				return nil, err
			}
		}
		if err := validPath(file.Path); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

// CommitFileDiff 使用与 CommitFiles 相同的首提交及第一父提交规则。
func (repo *Repository) CommitFileDiff(ctx context.Context, hash string, path string) (Diff, error) {
	if err := validCommitHash(hash); err != nil {
		return Diff{}, err
	}
	if err := validPath(path); err != nil {
		return Diff{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := repo.lockHistory(ctx); err != nil {
		return Diff{}, err
	}
	defer repo.mu.Unlock()
	files, err := repo.commitChanges(ctx, hash)
	if err != nil {
		return Diff{}, err
	}
	var selected *CommitFile
	for index := range files {
		if files[index].Path == path {
			selected = &files[index]
			break
		}
	}
	if selected == nil {
		for index := range files {
			if files[index].OriginalPath == path {
				selected = &files[index]
				break
			}
		}
	}
	if selected == nil {
		return Diff{}, errors.New("文件不在此提交的变更中")
	}
	args := []string{"-c", "core.quotePath=true", "show", "--no-show-signature", "--format=", "--root",
		"--diff-merges=first-parent", "--patch", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", "--no-relative",
		"--submodule=short", "--output-indicator-new=+", "--output-indicator-old=-", "--output-indicator-context= ",
		"--no-ext-diff", "--no-textconv", "--find-renames", hash, "--", selected.Path}
	// 保留重命名两端后再精确筛选 patch；字面 pathspec 仍会包含同名目录的后代。
	if selected.OriginalPath != "" {
		args = append(args, selected.OriginalPath)
	}
	diff, err := repo.selectedPatchPreview(ctx, args, *selected)
	if err != nil {
		return Diff{}, err
	}
	return repo.commitFileContents(ctx, diff, *selected, hash)
}

// selectedPatchPreview 读取并筛选单个文件的补丁；输出超过上限时截断展示。截断后若已读部分
// 中找不到该文件的补丁头，返回空的截断预览，而不是报告“无法定位”。
func (repo *Repository) selectedPatchPreview(ctx context.Context, args []string, file CommitFile) (Diff, error) {
	output, truncated, err := repo.gitPreview(ctx, args...)
	if err != nil {
		return Diff{}, err
	}
	output, err = selectedCommitPatch(output, file)
	if err != nil {
		if !truncated {
			return Diff{}, err
		}
		output = ""
	}
	return Diff{Text: output, Binary: strings.Contains(output, "Binary files "), Truncated: truncated}, nil
}

func selectedCommitPatch(output string, file CommitFile) (string, error) {
	original := file.Path
	if file.OriginalPath != "" {
		original = file.OriginalPath
	}
	header := "diff --git " + quoteCommitDiffPath("a/"+original) + " " + quoteCommitDiffPath("b/"+file.Path)
	var selected strings.Builder
	start := -1
	for cursor := 0; cursor < len(output); {
		end := strings.IndexByte(output[cursor:], '\n')
		if end < 0 {
			end = len(output)
		} else {
			end += cursor
		}
		line := output[cursor:end]
		if strings.HasPrefix(line, "diff --git ") {
			if start >= 0 {
				selected.WriteString(output[start:cursor])
			}
			start = -1
			if line == header {
				start = cursor
			}
		}
		cursor = end + 1
	}
	if start >= 0 {
		selected.WriteString(output[start:])
	}
	if selected.Len() == 0 {
		return "", errors.New("无法定位提交文件差异")
	}
	return selected.String(), nil
}

func quoteCommitDiffPath(path string) string {
	var quoted strings.Builder
	quoted.WriteByte('"')
	escaped := false
	for index := range len(path) {
		value := path[index]
		switch {
		case value == '"' || value == '\\':
			quoted.WriteByte('\\')
			quoted.WriteByte(value)
			escaped = true
		case value < ' ' || value >= 0x7f:
			special := strings.IndexByte("\a\b\t\n\v\f\r", value)
			if special >= 0 {
				quoted.WriteByte('\\')
				quoted.WriteByte("abtnvfr"[special])
			} else {
				fmt.Fprintf(&quoted, "\\%03o", value)
			}
			escaped = true
		default:
			quoted.WriteByte(value)
		}
	}
	if !escaped {
		return path
	}
	quoted.WriteByte('"')
	return quoted.String()
}
