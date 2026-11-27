// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os/exec"
	"path"
	"strings"
	"time"
)

func validSSHBrowsePath(name string) error {
	if name == "" || name == "." || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) || path.Clean(name) != name {
		return errors.New("请使用仓库内的规范相对路径")
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == ".." || strings.EqualFold(part, ".git") {
			return errors.New("文件路径超出仓库范围")
		}
	}
	return nil
}

func sshExitCode(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}

func (repo SSHWorkspace) head(ctx context.Context) (string, error) {
	output, err := repo.read(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	if sshExitCode(err, 1) {
		return "", nil
	}
	return strings.TrimSpace(output), err
}

func (repo SSHWorkspace) BrowseFiles(ctx context.Context, revision string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	hash, err := resolveBrowseRevision(ctx, revision, repo.head)
	if err != nil {
		return nil, err
	}
	return browsePaths(ctx, revision, hash, validSSHBrowsePath, repo.records)
}

func (repo SSHWorkspace) records(ctx context.Context, record func(string) error, args ...string) error {
	process, cancel := context.WithCancel(ctx)
	defer cancel()
	writer := nulRecordWriter{record: record, cancel: cancel}
	err := repo.gitTo(process, "", &writer, args...)
	if writer.err != nil && ctx.Err() == nil {
		return writer.err
	}
	if err != nil {
		return err
	}
	return writer.flush()
}

func (repo SSHWorkspace) BrowseFileContent(ctx context.Context, file, revision string) (*FileContent, error) {
	if err := validSSHBrowsePath(file); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	hash, err := resolveBrowseRevision(ctx, revision, repo.head)
	if err != nil {
		return nil, err
	}
	side := newImageAwareFileContent(file, revision)
	side.blameBase = hash
	switch revision {
	case "WORKTREE":
		if err := checkBrowseWorktreeMember(ctx, repo.read, file, hash); err != nil {
			return nil, err
		}
		return repo.readWorktreeFileContent(ctx, side)
	case "INDEX":
		content, err := readIndexFileContent(ctx, side, repo.read, repo.readBlobFileContent)
		if err == nil && !content.Present {
			return nil, errors.New("暂存区不存在该文件")
		}
		if err == nil && !content.Binary && !content.Truncated && !content.Unsupported {
			content.blameBase, err = repo.head(ctx)
		}
		return content, err
	default:
		return readTreeFileContent(ctx, side, hash, repo.read, repo.readBlobFileContent)
	}
}

func sshPatch(patch string) Diff {
	return Diff{Text: strings.ToValidUTF8(patch, "�"), Binary: strings.Contains(patch, "Binary files ")}
}

func (repo SSHWorkspace) diffWithPreviews(ctx context.Context, diff Diff, oldPath, oldRevision, newPath, newRevision string) (Diff, error) {
	preview, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	old, oldErr := repo.diffPreview(preview, oldPath, oldRevision)
	next, nextErr := repo.diffPreview(preview, newPath, newRevision)
	if oldErr == nil && nextErr == nil {
		diff.Old, diff.New = old, next
	}
	return diff, ctx.Err()
}

func (repo SSHWorkspace) diffPreview(ctx context.Context, file, revision string) (*FileContent, error) {
	if err := validSSHBrowsePath(file); err != nil {
		return nil, err
	}
	side := newImageAwareFileContent(file, revision)
	switch revision {
	case "":
		return side, nil
	case "WORKTREE":
		return repo.readWorktreeFileContent(ctx, side)
	case "INDEX":
		return readIndexFileContent(ctx, side, repo.read, repo.readBlobFileContent)
	default:
		hash, err := resolveBrowseRevision(ctx, revision, repo.head)
		if err != nil {
			return nil, err
		}
		side.blameBase = hash
		return readTreeFileContent(ctx, side, hash, repo.read, repo.readBlobFileContent)
	}
}

func (repo SSHWorkspace) readBlobFileContent(ctx context.Context, side *FileContent, mode, hash string, size int64) (*FileContent, error) {
	side.Present = true
	if mode != "100644" && mode != "100755" {
		side.Unsupported = true
		return side, nil
	}
	if err := validCommitHash(hash); err != nil {
		return nil, err
	}
	limit := side.readLimit()
	if size > int64(limit) {
		side.Truncated = true
		return side, nil
	}
	process, cancel := context.WithCancel(ctx)
	defer cancel()
	output := limitedBuffer{limit: limit, cancel: cancel}
	err := repo.gitTo(process, "", &output, "cat-file", "blob", hash)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if output.overflow {
		side.Truncated = true
		return side, nil
	}
	if err != nil {
		return nil, err
	}
	side.setContent(output.String())
	return side, nil
}

func (repo SSHWorkspace) readWorktreeFileContent(ctx context.Context, side *FileContent) (*FileContent, error) {
	parent, leaf := path.Dir(side.Path), "./"+path.Base(side.Path)
	var script strings.Builder
	if parent != "." {
		script.WriteString(`base=$PWD
gitDir=$(cd -P "$(git rev-parse --absolute-git-dir)" && pwd -P) || exit 2
commonDir=$(cd -P "$(git rev-parse --git-common-dir)" && pwd -P) || exit 2
expected="${base%/}/"` + sshShellWord(parent) + `
if ! cd -P ` + sshShellWord("./"+parent) + ` 2>/dev/null; then
    # 遇到不支持的祖先立即退出，避免穿过链接检查后续路径。
    remaining=` + sshShellWord(parent) + `
    cur=.
    while [ -n "$remaining" ]; do
        cur="$cur/${remaining%%/*}"
        [ ! -L "$cur" ] || exit 3
        [ -e "$cur" ] || exit 4
        [ -d "$cur" ] || exit 3
        [ -x "$cur" ] || exit 2
        case "$remaining" in */*) remaining=${remaining#*/};; *) break;; esac
    done
    exit 2
fi
[ "$PWD" = "$expected" ] || exit 3
case "$PWD/" in "$gitDir/"*|"$commonDir/"*) exit 3;; esac
`)
	}
	// tar 以物理模式归档单个条目。即使文件刚被换成链接或特殊文件，也不读取其目标或递归扫描目录。
	script.WriteString("file=" + sshShellWord(leaf) + `
[ ! -L "$file" ] || exit 5
[ -e "$file" ] || exit 4
unset TAR_OPTIONS
export COPYFILE_DISABLE=1
exec tar --no-recursion -cf - -- "$file"`)
	process, cancel := context.WithCancel(ctx)
	defer cancel()
	output := limitedBuffer{limit: side.readLimit() + (64 << 10), cancel: cancel}
	err := repo.script(process, script.String(), &output)
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case sshExitCode(err, 3):
		side.Unsupported = true
		return side, nil
	case sshExitCode(err, 4):
		return side, nil
	case sshExitCode(err, 5):
		side.Present, side.Unsupported = true, true
		return side, nil
	case err != nil && !output.overflow:
		return nil, err
	}
	reader := tar.NewReader(&output.content)
	header, err := reader.Next()
	if err != nil {
		return nil, errors.New("远端文件预览格式无效")
	}
	name := strings.TrimPrefix(header.Name, "./")
	if header.Typeflag == tar.TypeDir {
		name = strings.TrimSuffix(name, "/")
	}
	if name != path.Base(side.Path) {
		return nil, errors.New("远端文件预览格式无效")
	}
	side.Present = true
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
		side.Unsupported = true
		return side, nil
	}
	if header.Size > int64(side.readLimit()) {
		side.Truncated = true
		return side, nil
	}
	content, err := io.ReadAll(reader)
	if err != nil || output.overflow {
		return nil, errors.New("远端文件预览不完整")
	}
	side.setContent(string(content))
	return side, nil
}

func (repo SSHWorkspace) BlameLine(ctx context.Context, content *FileContent, line int) (LineBlame, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return readLineBlame(ctx, content, line, validSSHBrowsePath, repo.head, repo.readInput, sshExitCode)
}
