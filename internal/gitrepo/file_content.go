// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	fileContentLimit  = 1 << 20
	browserImageLimit = 12 << 20
)

// FileContent 描述 diff 一侧的只读完整版本；不可预览时 Text 为空，不能当作空文件。
type FileContent struct {
	Path        string `json:"path"`
	Revision    string `json:"revision"`
	Text        string `json:"text"`
	Present     bool   `json:"present"`
	Binary      bool   `json:"binary"`
	Truncated   bool   `json:"truncated"`
	Unsupported bool   `json:"unsupported"`
	ImageFormat string `json:"-"`
	ImageData   string `json:"-"`
	blameBase   string
}

func (content FileContent) SamePreview(other FileContent) bool {
	content.blameBase, other.blameBase = "", ""
	return content == other
}

func (repo *Repository) diffFileContents(ctx context.Context, diff Diff, selected File, staged bool) (Diff, error) {
	oldPath := selected.Path
	if staged && strings.ContainsAny(selected.Index, "RC") || !staged && strings.ContainsAny(selected.Working, "RC") {
		if selected.OriginalPath != "" {
			oldPath = selected.OriginalPath
		}
	}
	var err error
	if staged {
		head, headErr := repo.head(ctx)
		if headErr != nil {
			return Diff{}, headErr
		}
		diff.Old, err = repo.treeFileContent(ctx, oldPath, "HEAD", head)
	} else {
		diff.Old, err = repo.indexFileContent(ctx, oldPath)
	}
	if err != nil {
		return Diff{}, err
	}
	if staged {
		diff.New, err = repo.indexFileContent(ctx, selected.Path)
	} else {
		diff.New, err = repo.worktreeFileContent(ctx, selected.Path)
	}
	if err != nil {
		return Diff{}, err
	}
	diff.Generated = repo.isGeneratedDiff(ctx, diff)
	return diff, nil
}

func (repo *Repository) commitFileContents(ctx context.Context, diff Diff, selected CommitFile, hash string) (Diff, error) {
	parents, err := repo.commitParents(ctx, hash)
	if err != nil {
		return Diff{}, err
	}
	oldPath := selected.Path
	if selected.OriginalPath != "" {
		oldPath = selected.OriginalPath
	}
	parent := ""
	if len(parents) > 1 {
		parent = parents[1]
	}
	diff.Old, err = repo.treeFileContent(ctx, oldPath, parent, parent)
	if err != nil {
		return Diff{}, err
	}
	diff.New, err = repo.treeFileContent(ctx, selected.Path, parents[0], parents[0])
	if err != nil {
		return Diff{}, err
	}
	diff.Generated = repo.isGeneratedDiff(ctx, diff)
	return diff, nil
}

func (repo *Repository) treeFileContent(ctx context.Context, path, revision, hash string) (*FileContent, error) {
	return repo.readTreeFileContent(ctx, newImageAwareFileContent(path, revision), hash)
}

func (repo *Repository) readTreeFileContent(ctx context.Context, side *FileContent, hash string) (*FileContent, error) {
	path := side.Path
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if hash == "" {
		return side, nil
	}
	// -l 同时给出 blob 大小，省去单独的 cat-file -s。
	output, err := repo.git(ctx, "ls-tree", "-l", "-z", hash, "--", path)
	if err != nil {
		return nil, err
	}
	for _, record := range strings.Split(output, "\x00") {
		metadata, name, ok := strings.Cut(record, "\t")
		if !ok || name != path {
			continue
		}
		fields := strings.Fields(metadata)
		if len(fields) != 4 {
			return nil, errors.New("无法解析文件树对象")
		}
		// 目录本身不是 diff 的文件一侧（例如文件改为同名目录）。
		if fields[1] == "tree" {
			return side, nil
		}
		size := int64(-1)
		if fields[1] == "blob" {
			if size, err = strconv.ParseInt(fields[3], 10, 64); err != nil || size < 0 {
				return nil, errors.New("文件对象大小无效")
			}
		}
		return repo.blobFileContentSized(ctx, side, fields[0], fields[2], size)
	}
	return side, nil
}

func (repo *Repository) indexFileContent(ctx context.Context, path string) (*FileContent, error) {
	return repo.readIndexFileContent(ctx, newImageAwareFileContent(path, "INDEX"))
}

func (repo *Repository) readIndexFileContent(ctx context.Context, side *FileContent) (*FileContent, error) {
	path := side.Path
	output, err := repo.git(ctx, "ls-files", "--stage", "-z", "--", path)
	if err != nil {
		return nil, err
	}
	for _, record := range strings.Split(output, "\x00") {
		metadata, name, ok := strings.Cut(record, "\t")
		if !ok || name != path {
			continue
		}
		fields := strings.Fields(metadata)
		if len(fields) != 3 {
			return nil, errors.New("无法解析索引文件对象")
		}
		if fields[2] != "0" {
			// 冲突索引没有唯一完整版本，不能将某一冲突侧冒充 INDEX。
			side.Present, side.Unsupported = true, true
			return side, nil
		}
		return repo.blobFileContent(ctx, side, fields[0], fields[1])
	}
	return side, nil
}

func (repo *Repository) blobFileContent(ctx context.Context, side *FileContent, mode, hash string) (*FileContent, error) {
	return repo.blobFileContentSized(ctx, side, mode, hash, -1)
}

// blobFileContentSized 的 size 为 -1 时表示大小未知；已从 ls-tree -l 得到大小时可直接跳过大对象。
func (repo *Repository) blobFileContentSized(ctx context.Context, side *FileContent, mode, hash string, size int64) (*FileContent, error) {
	side.Present = true
	if mode != "100644" && mode != "100755" {
		side.Unsupported = true
		return side, nil
	}
	if content, ok := repo.cachedBlob(hash); ok {
		side.setContent(content)
		return side, nil
	}
	limit := side.readLimit()
	if size > int64(limit) {
		side.Truncated = true
		return side, nil
	}
	// 大小未知时不再单独 cat-file -s：带上限读取，超过预览上限即结束进程，
	// 同样不会把大 blob 载入内存；对象消失或读取失败仍向上报错。
	content, truncated, err := runCapped(ctx, repo.Path, "", 30*time.Second, limit, "cat-file", "blob", hash)
	if err != nil {
		return nil, err
	}
	if truncated {
		side.Truncated = true
		return side, nil
	}
	repo.rememberBlob(hash, content)
	side.setContent(content)
	return side, nil
}

func (side *FileContent) readLimit() int {
	if side.ImageFormat != "" && side.ImageFormat != "svg" {
		return browserImageLimit
	}
	return fileContentLimit
}

func (side *FileContent) setContent(content string) {
	if len(content) > side.readLimit() {
		side.Truncated = true
		return
	}
	if side.ImageFormat != "" && len(content) < 1024 && isLFSPointer([]byte(content)) {
		side.ImageFormat = ""
		side.Text = content
		return
	}
	if side.ImageFormat != "" && side.ImageFormat != "svg" {
		side.ImageData = content
		side.Binary = true
		return
	}
	side.Binary = strings.ContainsRune(content, 0) || !utf8.ValidString(content)
	if !side.Binary {
		side.Text = content
	}
}

func (repo *Repository) worktreeFileContent(ctx context.Context, path string) (*FileContent, error) {
	return repo.readWorktreeFileContent(ctx, newImageAwareFileContent(path, "WORKTREE"))
}

func (repo *Repository) readWorktreeFileContent(ctx context.Context, side *FileContent) (*FileContent, error) {
	path := side.Path
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(repo.Path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := repo.checkConflictAncestors(ctx, root, path); err != nil {
		if errors.Is(err, errUnsupportedAncestor) {
			// Git 将被替换目录中的跟踪项视为删除；不能穿过祖先链接读取替代内容。
			side.Unsupported = true
			return side, nil
		}
		return nil, err
	}
	info, err := root.Lstat(path)
	if os.IsNotExist(err) {
		return side, nil
	}
	if err != nil {
		return nil, err
	}
	side.Present = true
	if !info.Mode().IsRegular() {
		side.Unsupported = true
		return side, nil
	}
	limit := int64(side.readLimit())
	if info.Size() > limit {
		side.Truncated = true
		return side, nil
	}
	file, err := openFileContent(root, path, info)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	side.setContent(string(content))
	return side, nil
}

func openFileContent(root *os.Root, path string, info os.FileInfo) (*os.File, error) {
	if err := validPath(path); err != nil {
		return nil, err
	}
	if filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) != path {
		return nil, errors.New("请使用仓库内的规范相对路径")
	}
	// Lstat 与打开之间可能换成 FIFO 或链接；打开本身必须不阻塞且不跟随链接。
	file, err := openFileContentPath(root, path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("读取文件时内容已被替换，请刷新后重试")
	}
	return file, nil
}
