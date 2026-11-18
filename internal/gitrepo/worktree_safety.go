// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// worktreePath 只移除 Git 输出的一次换行由调用方完成，路径本身的换行必须保留。
func worktreePath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", errors.New("工作树路径无效")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent := absolute
	var missing []string
	for {
		_, err := os.Lstat(parent)
		if err == nil {
			canonical, err := filepath.EvalSymlinks(parent)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				canonical = filepath.Join(canonical, missing[index])
			}
			return filepath.Clean(canonical), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		missing = append(missing, filepath.Base(parent))
		next := filepath.Dir(parent)
		if next == parent {
			return "", errors.New("工作树路径没有可访问的父目录")
		}
		parent = next
	}
}

func worktreeCommonDir(ctx context.Context, path string) (string, error) {
	output, err := (&Repository{Path: path}).git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return worktreePath(strings.TrimSuffix(output, "\n"))
}

func parseWorktrees(output string) ([]Worktree, error) {
	if output == "" || !strings.HasSuffix(output, "\x00\x00") {
		return nil, errors.New("Git 工作树列表不完整")
	}
	result := []Worktree{}
	var current *Worktree
	for _, record := range strings.Split(output, "\x00") {
		if record == "" {
			current = nil
			continue
		}
		key, value, _ := strings.Cut(record, " ")
		if key == "worktree" {
			if current != nil || !filepath.IsAbs(value) {
				return nil, errors.New("无法解析 Git 工作树路径")
			}
			result = append(result, Worktree{Path: value, Main: len(result) == 0})
			current = &result[len(result)-1]
			continue
		}
		if current == nil {
			return nil, errors.New("Git 工作树记录缺少路径")
		}
		switch key {
		case "HEAD":
			if err := validCommitHash(value); err != nil {
				return nil, err
			}
			if strings.Trim(value, "0") != "" {
				current.Head = value
			}
		case "branch":
			current.Branch = value
		case "detached":
			current.Detached = true
		case "bare":
			current.Bare = true
		case "locked":
			current.Locked, current.LockReason = true, value
		case "prunable":
			current.Prunable, current.PruneReason = true, value
		}
	}
	return result, nil
}

// worktrees 完整检查全部工作树，供列表展示；破坏性操作只需相关条目，
// 应使用 worktreeEntries 加 inspectWorktrees 缩小范围。
func (repo *Repository) worktrees(ctx context.Context, common string) ([]worktreeState, error) {
	states, err := repo.worktreeEntries(ctx)
	if err != nil {
		return nil, err
	}
	if err := inspectWorktrees(ctx, common, states, func(Worktree) bool { return true }); err != nil {
		return nil, err
	}
	return states, nil
}

// inspectWorktrees 只完整检查 selected 选中的条目，每个约需 5 个 Git 进程并扫描整个工作区。
// 未选中的条目只有列表信息（Dirty、Operation 等未读取），不能用于授权破坏性操作，
// 也不应写入复核凭据。路径已有 Problem 的条目不再检查。
func inspectWorktrees(ctx context.Context, common string, states []worktreeState, selected func(Worktree) bool) error {
	for index := range states {
		state := &states[index]
		if state.Worktree.Problem == "" && selected(state.Worktree) {
			if err := state.inspect(ctx, common); err != nil {
				state.Worktree.Problem = err.Error()
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// worktreeEntries 只读取工作树列表并规范化路径，不为各工作树启动 Git 进程。
func (repo *Repository) worktreeEntries(ctx context.Context) ([]worktreeState, error) {
	output, err := repo.git(ctx, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	list, err := parseWorktrees(output)
	if err != nil {
		return nil, err
	}
	current, err := worktreePath(repo.Path)
	if err != nil {
		return nil, err
	}
	result := make([]worktreeState, 0, len(list))
	for _, worktree := range list {
		state := worktreeState{Worktree: worktree}
		canonical, pathErr := worktreePath(worktree.Path)
		if pathErr != nil {
			state.Worktree.Problem = "工作树路径无法访问：" + pathErr.Error()
		} else {
			state.Worktree.Path = canonical
			state.Worktree.Current = worktreeSamePath(canonical, current)
			info, linkErr := os.Lstat(worktree.Path)
			if linkErr == nil && info.Mode()&os.ModeSymlink != 0 {
				state.Worktree.Problem = "工作树目录已被符号链接替换，请先修复"
			}
		}
		result = append(result, state)
	}
	return result, nil
}

func (state *worktreeState) inspect(ctx context.Context, common string) error {
	root := state.Worktree.Path
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("工作树不存在或无法访问：%w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("工作树路径不是普通目录")
	}
	state.rootInfo = info
	state.RootStamp, err = worktreeFileIdentity(root, info)
	if err != nil {
		return err
	}
	state.RootStamp += fmt.Sprintf(":%d:%d", info.Mode(), info.ModTime().UnixNano())
	source := &Repository{Path: root}
	// 每个工作树都要读取，合并为一次 rev-parse；裸仓库没有工作区根目录，只读共享目录。
	options := []string{"--git-common-dir"}
	if !state.Worktree.Bare {
		options = append(options, "--show-toplevel", "--absolute-git-dir")
	}
	paths, err := source.revParsePaths(ctx, options...)
	if err != nil {
		return fmt.Errorf("无法读取工作树归属：%w", err)
	}
	actualCommon, err := worktreePath(paths[0])
	if err != nil {
		return fmt.Errorf("无法读取工作树归属：%w", err)
	}
	if !worktreeSamePath(actualCommon, common) {
		return errors.New("工作树目录已被其他仓库替换，Git common dir 不匹配")
	}
	state.commonInfo, err = os.Stat(common)
	if err != nil {
		return err
	}
	state.CommonStamp, err = worktreeFileIdentity(common, state.commonInfo)
	if err != nil {
		return err
	}
	if state.Worktree.Bare {
		return nil
	}
	canonical, err := worktreePath(paths[1])
	if err != nil || !worktreeSamePath(canonical, root) {
		return errors.New("工作树根目录已改变，请刷新核实")
	}
	state.GitDir, err = worktreePath(paths[2])
	if err != nil {
		return err
	}
	state.gitDirInfo, err = os.Stat(state.GitDir)
	if err != nil {
		return err
	}
	state.GitDirStamp, err = worktreeFileIdentity(state.GitDir, state.gitDirInfo)
	if err != nil {
		return err
	}
	if !state.Worktree.Main {
		if err := state.inspectLink(common); err != nil {
			return err
		}
	} else if err := state.inspectGitFile(); err != nil {
		return err
	}
	head, err := source.head(ctx)
	if err != nil {
		return err
	}
	branch, branchErr := source.git(ctx, "symbolic-ref", "--quiet", "HEAD")
	if branchErr != nil && !exitCode(branchErr, 1) {
		return branchErr
	}
	branch = strings.TrimSuffix(branch, "\n")
	detached := exitCode(branchErr, 1)
	if head != state.Worktree.Head || branch != state.Worktree.Branch || detached != state.Worktree.Detached {
		return errors.New("工作树 HEAD 或分支在读取期间已改变，请刷新")
	}
	operation, err := source.operationIn(ctx, state.GitDir)
	if err != nil {
		return err
	}
	state.Worktree.Operation, state.OperationToken = operation.Kind, operation.Token
	// 禁用可选索引写入及状态缓存，读取不得刷新索引或漏掉未跟踪、忽略文件。
	// --ignored=matching 会逐个列出 node_modules 等目录下的文件，输出可远超 4 MiB；
	// 流式解析并只保留摘要，复核凭据仍随任一文件状态变化而改变。
	digest := sha256.New()
	statusRecords := &nulRecords{}
	pendingOriginal := false
	statusRecords.handle = func(prefix []byte, length int) error {
		if pendingOriginal {
			pendingOriginal = false
			if length == 0 {
				return errors.New("工作树重命名状态不完整")
			}
			return nil
		}
		if length == 0 {
			return nil
		}
		if length < 4 || prefix[2] != ' ' {
			return errors.New("无法解析工作树文件状态")
		}
		if string(prefix[:2]) == "!!" {
			state.Worktree.Ignored = true
		} else {
			state.Worktree.Dirty = true
		}
		pendingOriginal = bytes.ContainsAny(prefix[:2], "RC")
		return nil
	}
	err = runTo(ctx, root, "", 30*time.Second, io.MultiWriter(digest, statusRecords), func() string { return "" },
		"--no-optional-locks", "-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false", "-c", "core.ignoreStat=false", "status",
		"--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if err := statusRecords.finish(); err != nil {
		return err
	}
	if pendingOriginal {
		return errors.New("工作树重命名状态不完整")
	}
	state.Status = hex.EncodeToString(digest.Sum(nil))
	// 约 8 万个文件时输出即超过 4 MiB，因此逐条只检查首字节标记，不保留全文。
	flagged := false
	flags := &nulRecords{handle: func(prefix []byte, length int) error {
		// Git status 会隐藏这些索引条目的本地编辑，不能据此授权删除数据。
		if length > 0 && (prefix[0] == 'S' || prefix[0] >= 'a' && prefix[0] <= 'z') {
			flagged = true
		}
		return nil
	}}
	if err := runTo(ctx, root, "", 30*time.Second, flags, func() string { return "" }, "ls-files", "-v", "-z"); err != nil {
		return err
	}
	if err := flags.finish(); err != nil {
		return err
	}
	if flagged {
		return errors.New("索引含有 assume-unchanged 或 skip-worktree 条目，无法确认工作树数据已保存")
	}
	return nil
}

// nulRecords 流式切分以 NUL 结尾的记录，每条只保留前 4 字节和长度，输出再大也只占常量内存。
// Write 从不返回错误：exec 的复制协程遇到写错误会停止读取，子进程可能阻塞在满管道上直到超时；
// 首个解析错误被记录下来，由调用方在命令结束后通过 finish 取得。
type nulRecords struct {
	handle func(prefix []byte, length int) error
	prefix []byte
	length int
	err    error
}

func (records *nulRecords) Write(data []byte) (int, error) {
	size := len(data)
	for len(data) > 0 && records.err == nil {
		end := bytes.IndexByte(data, 0)
		chunk := data
		if end >= 0 {
			chunk = data[:end]
		}
		if room := 4 - len(records.prefix); room > 0 {
			records.prefix = append(records.prefix, chunk[:min(room, len(chunk))]...)
		}
		records.length += len(chunk)
		if end < 0 {
			break
		}
		records.err = records.handle(records.prefix, records.length)
		records.prefix, records.length = records.prefix[:0], 0
		data = data[end+1:]
	}
	return size, nil
}

// finish 处理末尾缺少 NUL 的残余记录，并返回解析期间的首个错误。
func (records *nulRecords) finish() error {
	if records.err == nil && records.length > 0 {
		records.err = records.handle(records.prefix, records.length)
		records.prefix, records.length = records.prefix[:0], 0
	}
	return records.err
}

func (state *worktreeState) inspectLink(common string) error {
	relative, err := filepath.Rel(filepath.Join(common, "worktrees"), state.GitDir)
	if err != nil || relative == "." || filepath.Base(relative) != relative || relative == ".." {
		return errors.New("链接工作树的 Git 元数据不属于当前仓库")
	}
	if err := state.inspectGitFile(); err != nil {
		return err
	}
	gitFile := filepath.Join(state.Worktree.Path, ".git")
	backlink, err := os.ReadFile(filepath.Join(state.GitDir, "gitdir"))
	if err != nil {
		return err
	}
	backPath, err := worktreePath(strings.TrimSuffix(string(backlink), "\n"))
	if err != nil || !worktreeSamePath(backPath, gitFile) {
		return errors.New("链接工作树目录与 Git 元数据的反向关联不匹配")
	}
	return nil
}

func (state *worktreeState) inspectGitFile() error {
	gitFile := filepath.Join(state.Worktree.Path, ".git")
	info, err := os.Lstat(gitFile)
	if err != nil || !info.Mode().IsRegular() && !(state.Worktree.Main && info.IsDir()) {
		return errors.New("工作树 .git 元数据已缺失或被替换")
	}
	state.gitFileInfo = info
	state.GitFileStamp, err = worktreeFileIdentity(gitFile, info)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	state.GitFileStamp += fmt.Sprintf(":%d:%d", info.Mode(), info.ModTime().UnixNano())
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return err
	}
	state.GitFile = string(data)
	return nil
}

func (state worktreeState) checkIdentity(ctx context.Context, common string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(state.Worktree.Path)
	if err != nil || state.rootInfo == nil || !os.SameFile(info, state.rootInfo) || !info.IsDir() {
		return errors.New("工作树目录在复核后已被替换，请刷新")
	}
	actualCommon, err := worktreeCommonDir(ctx, state.Worktree.Path)
	if err != nil {
		return err
	}
	if !worktreeSamePath(actualCommon, common) {
		return errors.New("工作树 Git common dir 已改变，请刷新")
	}
	// 路径及内容可被原样复制，确认还必须绑定实际 Git 元数据目录的身份。
	commonInfo, err := os.Stat(common)
	if err != nil || state.commonInfo == nil || !os.SameFile(commonInfo, state.commonInfo) {
		return errors.New("共享 Git 元数据目录在复核后已被替换，请刷新")
	}
	gitDirInfo, err := os.Stat(state.GitDir)
	if err != nil || state.gitDirInfo == nil || !os.SameFile(gitDirInfo, state.gitDirInfo) {
		return errors.New("工作树 Git 元数据目录在复核后已被替换，请刷新")
	}
	gitFile := filepath.Join(state.Worktree.Path, ".git")
	info, err = os.Lstat(gitFile)
	if err != nil || state.gitFileInfo == nil || !os.SameFile(info, state.gitFileInfo) ||
		!info.Mode().IsRegular() && !(state.Worktree.Main && info.IsDir()) {
		return errors.New("工作树 .git 文件在复核后已被替换，请刷新")
	}
	if info.IsDir() {
		return nil
	}
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return err
	}
	if string(data) != state.GitFile {
		return errors.New("工作树 Git 关联在复核后已改变，请刷新")
	}
	return nil
}

func worktreeContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return true
	}
	// macOS 的大小写别名不能绕过包含关系；缺失的末级路径从现存祖先核对。
	for current := child; ; current = filepath.Dir(current) {
		if worktreeSamePath(parent, current) {
			return true
		}
		if filepath.Dir(current) == current {
			return false
		}
	}
}

func worktreeSamePath(first, second string) bool {
	if first == second {
		return true
	}
	firstInfo, firstErr := os.Stat(first)
	secondInfo, secondErr := os.Stat(second)
	return firstErr == nil && secondErr == nil && os.SameFile(firstInfo, secondInfo)
}

func worktreeCreatePath(path, common string, states []worktreeState) (string, error) {
	original, err := filepath.Abs(path)
	if path == "" || err != nil || strings.ContainsRune(path, 0) {
		return "", errors.New("工作树路径无效")
	}
	for current := original; ; current = filepath.Dir(current) {
		if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
			// 系统根目录下的路径别名（如 /var）可规范化，其余链接不作为创建入口。
			root := filepath.VolumeName(current) + string(filepath.Separator)
			if current == original || filepath.Dir(current) != root {
				return "", errors.New("不能在符号链接位置创建工作树")
			}
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	canonical, err := worktreePath(original)
	if err != nil {
		return "", err
	}
	for _, candidate := range []string{original, canonical} {
		for _, part := range strings.Split(filepath.ToSlash(candidate), "/") {
			if strings.EqualFold(part, ".git") {
				return "", errors.New("不能在 Git 元数据中创建工作树")
			}
		}
	}
	if worktreeContains(common, canonical) || worktreeContains(canonical, common) {
		return "", errors.New("工作树路径与 Git 元数据目录重叠")
	}
	for _, state := range states {
		if worktreeContains(state.Worktree.Path, canonical) || worktreeContains(canonical, state.Worktree.Path) {
			return "", errors.New("工作树路径不能与当前或其他已知工作树重叠")
		}
	}
	info, err := os.Lstat(canonical)
	if os.IsNotExist(err) {
		return canonical, nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("工作树路径已被文件或符号链接占用")
	}
	directory, err := os.Open(canonical)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	_, err = directory.Readdirnames(1)
	if !errors.Is(err, io.EOF) {
		if err != nil {
			return "", err
		}
		return "", errors.New("工作树路径已存在且不是空目录")
	}
	return canonical, nil
}
