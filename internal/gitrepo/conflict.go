// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const conflictTextLimit = 1 << 20

var errUnsupportedAncestor = errors.New("冲突路径的父目录不能是符号链接或普通文件")

type ConflictSide struct {
	Present bool   `json:"present"`
	Text    string `json:"text"`
	Binary  bool   `json:"binary"`
	Mode    string `json:"mode"`
	Hash    string `json:"hash"`
}

type Conflict struct {
	Path       string       `json:"path"`
	Token      string       `json:"token"`
	Base       ConflictSide `json:"base"`
	Ours       ConflictSide `json:"ours"`
	Theirs     ConflictSide `json:"theirs"`
	Result     string       `json:"result"`
	Editable   bool         `json:"editable"`
	MarkerSize int          `json:"markerSize"`
}

func (repo *Repository) Conflict(ctx context.Context, path string) (Conflict, error) {
	// 只读载入与 Diff 等读取入口一致，等待仓库锁时也响应取消。
	if err := repo.lockHistory(ctx); err != nil {
		return Conflict{}, err
	}
	defer repo.mu.Unlock()
	return repo.conflict(ctx, path)
}

func (repo *Repository) conflict(ctx context.Context, path string) (Conflict, error) {
	view := Conflict{Path: path}
	if err := validPath(path); err != nil {
		return view, err
	}
	stages, err := repo.git(ctx, "ls-files", "--unmerged", "-z", "--", path)
	if err != nil {
		return view, err
	}
	if stages == "" {
		return view, errors.New("此文件已无冲突，请刷新")
	}
	for _, record := range strings.Split(strings.TrimSuffix(stages, "\x00"), "\x00") {
		metadata, name, ok := strings.Cut(record, "\t")
		fields := strings.Fields(metadata)
		if !ok || name != path || len(fields) != 3 {
			return view, errors.New("无法解析冲突版本")
		}
		side := ConflictSide{Present: true, Mode: fields[0], Hash: fields[1]}
		if side.Mode == "160000" {
			return view, errors.New("子模块冲突请在外部 Git 工具中解决后刷新")
		}
		// 一次带上限的读取代替先查大小再读内容；超过上限即结束进程并按二进制处理。
		content, truncated, err := runCapped(ctx, repo.Path, "", 30*time.Second, conflictTextLimit, "cat-file", "blob", side.Hash)
		if err != nil {
			return view, err
		}
		side.Binary = truncated || strings.ContainsRune(content, 0) || !utf8.ValidString(content)
		if !side.Binary {
			side.Text = content
		}
		switch fields[2] {
		case "1":
			view.Base = side
		case "2":
			view.Ours = side
		case "3":
			view.Theirs = side
		default:
			return view, errors.New("冲突阶段无效")
		}
	}
	root, err := os.OpenRoot(repo.Path)
	if err != nil {
		return view, err
	}
	defer root.Close()
	if err := repo.checkConflictAncestors(ctx, root, path); err != nil {
		return view, err
	}
	digest := sha256.New()
	fmt.Fprint(digest, stages)
	info, err := root.Lstat(path)
	if os.IsNotExist(err) {
		fmt.Fprint(digest, "\x00missing")
		view.Editable = true
	} else if err != nil {
		return view, err
	} else if info.Mode().IsRegular() {
		file, err := root.Open(path)
		if err != nil {
			return view, err
		}
		defer file.Close()
		fmt.Fprintf(digest, "\x00%v\x00", info.Mode())
		content, err := io.ReadAll(io.LimitReader(io.TeeReader(file, digest), conflictTextLimit+1))
		if err != nil {
			return view, err
		}
		if _, err := io.Copy(digest, file); err != nil {
			return view, err
		}
		view.Editable = len(content) <= conflictTextLimit && utf8.Valid(content) && !strings.ContainsRune(string(content), 0)
		if view.Editable {
			view.Result = string(content)
		}
	} else if info.Mode()&os.ModeSymlink != 0 {
		target, err := root.Readlink(path)
		if err != nil {
			return view, err
		}
		fmt.Fprintf(digest, "\x00symlink\x00%s", target)
	} else {
		return view, errors.New("此冲突文件类型不支持在应用内处理")
	}
	view.Editable = view.Editable && (!view.Ours.Present || view.Ours.Mode != "120000") && (!view.Theirs.Present || view.Theirs.Mode != "120000")
	if view.Editable {
		attribute, err := repo.git(ctx, "check-attr", "-z", "conflict-marker-size", "--", path)
		if err != nil {
			return view, err
		}
		_, value, _ := strings.Cut(attribute, "\x00conflict-marker-size\x00")
		view.MarkerSize, _ = strconv.Atoi(strings.TrimSuffix(value, "\x00"))
		if view.MarkerSize <= 0 {
			view.MarkerSize = 7
		}
		fmt.Fprintf(digest, "\x00%d", view.MarkerSize)
	}
	head, err := repo.head(ctx)
	if err != nil {
		return view, err
	}
	fmt.Fprint(digest, "\x00"+head)
	view.Token = hex.EncodeToString(digest.Sum(nil))
	return view, nil
}

func (repo *Repository) checkConflictAncestors(ctx context.Context, root *os.Root, path string) error {
	return repo.newAncestorGuard().check(ctx, root, path)
}

// ancestorGuard 在一次检查内复用 Git 元数据目录信息，逐个检查多个路径时只解析一次。
// 只在单次调用内复用而不缓存在 Repository 上：子模块吸收、工作树迁移等会改变 Git 目录，
// 长期缓存的路径会使检查失效或永久失败。
type ancestorGuard struct {
	repo     *Repository
	metadata []os.FileInfo
}

func (repo *Repository) newAncestorGuard() *ancestorGuard {
	return &ancestorGuard{repo: repo}
}

func (guard *ancestorGuard) check(ctx context.Context, root *os.Root, path string) error {
	parent := ""
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(filepath.FromSlash(path))), "/") {
		if part == "." {
			continue
		}
		// 仅在存在祖先目录时才需要元数据目录，顶层文件不必读取。
		if guard.metadata == nil {
			metadata, err := guard.repo.metadataInfos(ctx)
			if err != nil {
				return err
			}
			guard.metadata = metadata
		}
		parent = filepath.Join(parent, part)
		info, err := root.Lstat(parent)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		// Root 允许仓库内链接，因此还需拒绝链接祖先及实际 Git 元数据目录。
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errUnsupportedAncestor
		}
		for _, directory := range guard.metadata {
			if os.SameFile(info, directory) {
				return errors.New("不能操作 Git 元数据目录中的冲突文件")
			}
		}
	}
	return nil
}

// metadataInfos 返回 Git 目录与共享目录的文件信息，只用于按文件身份比较。
// 常见布局直接从文件系统解析，无法确定时才启动 rev-parse。
func (repo *Repository) metadataInfos(ctx context.Context) ([]os.FileInfo, error) {
	directories, ok := localMetadataDirectories(repo.Path)
	if !ok {
		var err error
		if directories, err = repo.metadataDirectories(ctx); err != nil {
			return nil, err
		}
	}
	metadata := make([]os.FileInfo, 0, len(directories))
	for _, directory := range directories {
		info, err := os.Stat(directory)
		if err != nil {
			return nil, err
		}
		metadata = append(metadata, info)
	}
	return metadata, nil
}

// localMetadataDirectories 按 Git 的规则解析 Git 目录及其 commondir：
// commondir 不存在时共享目录就是 Git 目录；文件内容异常时返回 false 交给 Git。
func localMetadataDirectories(root string) ([]string, bool) {
	directory, ok := localGitDirectory(root)
	if !ok {
		return nil, false
	}
	content, err := os.ReadFile(filepath.Join(directory, "commondir"))
	if os.IsNotExist(err) {
		return []string{directory, directory}, true
	}
	if err != nil || len(content) > 4096 {
		return nil, false
	}
	common := strings.TrimRight(string(content), "\r\n")
	if common == "" || strings.ContainsAny(common, "\x00\n\r") {
		return nil, false
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(directory, common)
	}
	return []string{directory, filepath.Clean(common)}, true
}

// metadataDirectories 读取当前 Git 目录与共享目录。
func (repo *Repository) metadataDirectories(ctx context.Context) ([]string, error) {
	return repo.revParsePaths(ctx, "--absolute-git-dir", "--git-common-dir")
}

// revParsePaths 用一次 rev-parse 按顺序读取多个绝对路径；路径含换行无法按行拆分时逐个读取。
func (repo *Repository) revParsePaths(ctx context.Context, options ...string) ([]string, error) {
	output, err := repo.git(ctx, append([]string{"rev-parse", "--path-format=absolute"}, options...)...)
	if err != nil {
		return nil, err
	}
	if lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n"); len(lines) == len(options) {
		return lines, nil
	}
	var paths []string
	for _, option := range options {
		path, err := repo.git(ctx, "rev-parse", "--path-format=absolute", option)
		if err != nil {
			return nil, err
		}
		paths = append(paths, strings.TrimSuffix(path, "\n"))
	}
	return paths, nil
}

// ConflictMarker 识别不含行尾的 Git 冲突标记行，返回标记字符。
func ConflictMarker(line string, markerSize int) byte {
	if markerSize <= 0 {
		markerSize = 7
	}
	if len(line) < markerSize {
		return 0
	}
	marker := line[0]
	switch marker {
	case '<', '>', '|', '=':
		for index := 1; index < markerSize; index++ {
			if line[index] != marker {
				return 0
			}
		}
		if len(line) == markerSize || marker != '=' && line[markerSize] == ' ' {
			return marker
		}
	}
	return 0
}

func hasConflictMarkers(content string, markerSize int) bool {
	for line := range strings.FieldsFuncSeq(content, func(char rune) bool { return char == '\r' || char == '\n' }) {
		switch ConflictMarker(line, markerSize) {
		case '<', '>', '|':
			return true
		}
	}
	return false
}

func (repo *Repository) ResolveConflict(ctx context.Context, path, token, choice, content string) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	view, err := repo.conflict(ctx, path)
	if err != nil {
		return err
	}
	if token == "" || view.Token != token {
		return errors.New("冲突文件已在外部改变，请重新载入后再解决")
	}
	root, err := os.OpenRoot(repo.Path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := repo.checkConflictAncestors(ctx, root, path); err != nil {
		return err
	}
	switch choice {
	case "ours", "theirs":
		stage, side := "2", view.Ours
		if choice == "theirs" {
			stage, side = "3", view.Theirs
		}
		if !side.Present {
			if err := root.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		} else if _, err := repo.git(ctx, "checkout-index", "--force", "--stage="+stage, "--", path); err != nil {
			return err
		}
	case "delete":
		if err := root.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	case "result":
		if !view.Editable || len(content) > conflictTextLimit || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
			return errors.New("此文件不支持文本编辑，请选择保留一个版本")
		}
		if hasConflictMarkers(content, view.MarkerSize) {
			return errors.New("结果仍包含冲突标记，请先完成编辑")
		}
		mode := os.FileMode(0644)
		if info, err := root.Lstat(path); err == nil {
			mode = info.Mode().Perm()
		} else if view.Ours.Mode == "100755" || view.Theirs.Mode == "100755" {
			mode = 0755
		}
		if err := root.MkdirAll(filepath.Dir(filepath.FromSlash(path)), 0755); err != nil {
			return err
		}
		if err := repo.checkConflictAncestors(ctx, root, path); err != nil {
			return err
		}
		temp := filepath.Join(filepath.Dir(path), ".xsourcetree-resolve-"+rand.Text())
		file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		defer root.Remove(temp)
		if err := file.Chmod(mode); err != nil {
			file.Close()
			return err
		}
		_, writeErr := file.WriteString(content)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := root.Rename(temp, path); err != nil {
			return err
		}
	default:
		return errors.New("不支持的冲突解决方式")
	}
	_, err = repo.git(ctx, "add", "--", path)
	return err
}
