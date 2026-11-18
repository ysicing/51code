// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// readHeadCommit 不启动 Git，直接从仓库元数据解析工作区 HEAD 指向的提交。
// 只处理常见布局：.git 目录或 gitdir 文件（链接工作区、子模块）、松散引用和 packed-refs。
// reftable、符号引用链、未出生分支等无法确定的情况返回 false，由调用方退回 Git 命令；
// 因此结果只能用来判断"与缓存相同"，不能替代 Git 做仓库校验。
func readHeadCommit(worktree string) (string, bool) {
	gitDir := filepath.Join(worktree, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return "", false
	}
	if !info.IsDir() {
		content, err := readSmallFile(gitDir)
		target, found := strings.CutPrefix(strings.TrimSpace(content), "gitdir: ")
		if err != nil || !found || target == "" {
			return "", false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(worktree, target)
		}
		gitDir = target
	}
	commonDir := gitDir
	if content, err := readSmallFile(filepath.Join(gitDir, "commondir")); err == nil {
		commonDir = strings.TrimSpace(content)
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(commonDir, "reftable")); !errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	content, err := readSmallFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	head := strings.TrimSpace(content)
	ref, symbolic := strings.CutPrefix(head, "ref: ")
	if !symbolic {
		return head, validObjectID(head)
	}
	// 分支引用存放在共享目录；其他引用可能按工作区存放，交给 Git 解析。
	if !strings.HasPrefix(ref, "refs/heads/") || !fs.ValidPath(ref) {
		return "", false
	}
	content, err = readSmallFile(filepath.Join(commonDir, filepath.FromSlash(ref)))
	if err == nil {
		id := strings.TrimSpace(content)
		return id, validObjectID(id)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	return packedRefCommit(filepath.Join(commonDir, "packed-refs"), ref)
}

func packedRefCommit(file, ref string) (string, bool) {
	handle, err := os.Open(file)
	if err != nil {
		return "", false
	}
	defer handle.Close()
	scanner := bufio.NewScanner(handle)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' || line[0] == '^' {
			continue
		}
		id, name, found := strings.Cut(line, " ")
		if found && name == ref {
			return id, validObjectID(id)
		}
	}
	return "", false
}

func readSmallFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	const limit = 4 << 10
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if len(data) > limit {
		return "", errors.New("文件过大")
	}
	return string(data), nil
}

// validObjectID 只接受 Git 输出的小写 SHA-1 或 SHA-256 对象 ID。
func validObjectID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, char := range id {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
