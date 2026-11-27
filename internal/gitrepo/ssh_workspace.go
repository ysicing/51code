// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

type SSHWorkspace struct {
	Host string `json:"host"`
	Path string `json:"path"`
}

var sshWorkspaceHost = regexp.MustCompile(`^([a-zA-Z0-9_][a-zA-Z0-9_.-]*@)?([a-zA-Z0-9_][a-zA-Z0-9_.-]*|\[[0-9a-fA-F:]+\])$`)

func ValidSSHHost(host string) bool {
	return sshWorkspaceHost.MatchString(host)
}

func sshShellWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func validSSHPath(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.ContainsAny(value, "\x00\r\n")
}

type SSHDirectory struct {
	Path     string
	Children []string
}

func ReadSSHDirectory(ctx context.Context, host, directory string) (SSHDirectory, error) {
	target := `"${HOME:?}"`
	if directory != "" {
		if !validSSHPath(directory) {
			return SSHDirectory{}, errors.New("请输入远端工作区的绝对路径")
		}
		target = sshShellWord(directory)
	}
	script := `unset CDPATH
cd -P ` + target + ` || exit 2
printf '%s\000' "$PWD"
for child in ./.[!.]* ./..?* ./*; do
    [ "$child" = "./.git" ] && continue
    if [ -d "$child" ]; then
        printf '%s\000' "${child#./}"
    fi
done
exit 0`
	raw, err := runSSHScript(ctx, host, script)
	if err != nil {
		return SSHDirectory{}, err
	}
	fields := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	if !strings.HasSuffix(raw, "\x00") || !validSSHPath(fields[0]) {
		return SSHDirectory{}, errors.New("无法读取远端目录")
	}
	result := SSHDirectory{Path: fields[0]}
	for _, name := range fields[1:] {
		if name != "" && name != "." && name != ".." && !strings.Contains(name, "/") {
			child := path.Join(result.Path, name)
			if validSSHPath(child) {
				result.Children = append(result.Children, child)
			}
		}
	}
	slices.Sort(result.Children)
	return result, nil
}

func (repo SSHWorkspace) read(ctx context.Context, args ...string) (string, error) {
	return repo.readInput(ctx, "", args...)
}

func (repo SSHWorkspace) gitPreview(ctx context.Context, args ...string) (string, bool, error) {
	process, cancel := context.WithCancel(ctx)
	defer cancel()
	output := limitedBuffer{cancel: cancel}
	err := repo.gitTo(process, "", &output, args...)
	if output.overflow {
		return strings.ToValidUTF8(trimPartialLine(output.String()), "�"), true, ctx.Err()
	}
	return strings.ToValidUTF8(output.String(), "�"), false, err
}

func (repo SSHWorkspace) readInput(ctx context.Context, input string, args ...string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := limitedBuffer{cancel: cancel}
	err := repo.gitTo(ctx, input, &output, args...)
	if output.overflow {
		return "", errOutputTooLarge
	}
	return output.String(), err
}

func (repo SSHWorkspace) script(ctx context.Context, command string, output io.Writer) error {
	return repo.scriptTimeout(ctx, command, output, 30*time.Second)
}

func (repo SSHWorkspace) scriptTimeout(ctx context.Context, command string, output io.Writer, timeout time.Duration) error {
	if !validSSHPath(repo.Path) {
		return errors.New("请输入远端工作区的绝对路径")
	}
	prefix := "export LC_ALL=C\nunset CDPATH\ncd -P " + sshShellWord(repo.Path) + " || exit 2\n" +
		"root=$(git rev-parse --show-toplevel) && cd -P \"$root\" || exit 2\n"
	return runSSHTimeout(ctx, repo.Host, prefix+command, output, timeout)
}

const sshGit = "git --no-pager --no-optional-locks --literal-pathspecs -c core.fsmonitor=false -c color.ui=false -c core.quotePath=false"

func (repo SSHWorkspace) gitTo(ctx context.Context, input string, output io.Writer, args ...string) error {
	var script strings.Builder
	if input != "" {
		script.WriteString("printf '%s' " + sshShellWord(input) + " | ")
	}
	script.WriteString("exec " + sshGit)
	for _, arg := range args {
		script.WriteByte(' ')
		script.WriteString(sshShellWord(arg))
	}
	return repo.script(ctx, script.String(), output)
}

func runSSHScript(ctx context.Context, host, script string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := limitedBuffer{cancel: cancel}
	err := runSSH(ctx, host, script, &output)
	if output.overflow {
		return "", errOutputTooLarge
	}
	return output.String(), err
}

func runSSH(ctx context.Context, host, script string, stdout io.Writer) error {
	return runSSHTimeout(ctx, host, script, stdout, 30*time.Second)
}

func runSSHTimeout(ctx context.Context, host, script string, stdout io.Writer, timeout time.Duration) error {
	if strings.ContainsRune(script, 0) {
		return errors.New("SSH 脚本含有无效字符")
	}
	if !ValidSSHHost(host) {
		return errors.New("SSH 主机须为配置别名、主机名或 user@host")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-T", "-a", "-x", "-oBatchMode=yes",
		"-oStrictHostKeyChecking=yes", "-oClearAllForwardings=yes", "-oConnectTimeout=10", "--", host, "sh -s")
	cmd.Stdin = strings.NewReader(script + "\n")
	cmd.WaitDelay = time.Second
	stderr := limitedBuffer{limit: 16 << 10}
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	err := runProcess(ctx, cmd)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return fmt.Errorf("SSH: %s: %w", message, err)
		}
	}
	return err
}

func (repo SSHWorkspace) Snapshot(ctx context.Context) (Snapshot, []string, error) {
	raw, err := repo.read(ctx, "status", "--porcelain=v2", "-z", "--branch", "--ahead-behind", "--untracked-files=all")
	if err != nil {
		return Snapshot{}, nil, err
	}
	branch, files, err := parseStatus(raw)
	if err != nil {
		return Snapshot{}, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	refs := limitedBuffer{cancel: cancel}
	ref := ""
	if !branch.detached && branch.head != "" {
		ref = "refs/heads/" + branch.branch
	}
	err = repo.script(ctx, `gitdir=$(git rev-parse --absolute-git-dir) && common=$(git rev-parse --git-common-dir) || exit 2
printf '%s\n' "$PWD"
if [ -w . ] && [ -w "$gitdir" ] && [ -w "$common" ] && [ -w "$common/objects" ]; then printf 'writable\n'; else printf 'readonly\n'; fi
ref=`+sshShellWord(ref)+`
upstream=''
if [ -n "$ref" ]; then
    upstream=$(git for-each-ref '--format=%(upstream:short)%09%(upstream:remotename)%09%(upstream:remoteref)' -- "$ref") || exit 2
fi
printf '%s\n' "$upstream"
exec `+sshGit+` for-each-ref '--format=%(refname)' refs/heads refs/remotes refs/tags`, &refs)
	if refs.overflow {
		return Snapshot{}, nil, errOutputTooLarge
	}
	if err != nil {
		return Snapshot{}, nil, err
	}
	header := strings.SplitN(refs.String(), "\n", 4)
	if len(header) != 4 || !validSSHPath(header[0]) {
		return Snapshot{}, nil, errors.New("无法读取远端工作区根路径")
	}
	upstream, rest, _ := strings.Cut(header[2], "\t")
	remote, remoteRef, _ := strings.Cut(rest, "\t")
	if branch.detached {
		branch.branch = branch.head[:min(8, len(branch.head))]
	}
	return Snapshot{Path: header[0], Branch: branch.branch, Head: branch.head, Files: files, Detached: branch.detached,
		Unborn: branch.head == "", Upstream: upstream, Ahead: branch.ahead, Behind: branch.behind,
		SSHWritable: header[1] == "writable", SSHRemote: remote, SSHRemoteRef: remoteRef}, strings.Fields(header[3]), nil
}

func (repo SSHWorkspace) History(ctx context.Context, ref string) ([]Commit, error) {
	return repo.readHistory(ctx, ref, "")
}

func (repo SSHWorkspace) FileHistory(ctx context.Context, revision, file string) ([]Commit, error) {
	if err := validSSHBrowsePath(file); err != nil {
		return nil, err
	}
	switch revision {
	case "WORKTREE", "HEAD", "INDEX":
		revision = ""
	default:
		if err := validCommitHash(revision); err != nil {
			return nil, err
		}
	}
	return repo.readHistory(ctx, revision, file)
}

func (repo SSHWorkspace) readHistory(ctx context.Context, ref, file string) ([]Commit, error) {
	return repo.HistoryPage(ctx, ref, "", file, HistoryFilter{}, 0, DefaultHistoryPageSize)
}

func (repo SSHWorkspace) HistoryPage(ctx context.Context, ref, query, file string, filter HistoryFilter, skip, limit int) ([]Commit, error) {
	if skip < 0 || limit < 1 || limit > MaxHistoryReloadSize || strings.ContainsRune(query, 0) {
		return nil, errors.New("无效的历史查询")
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	if file != "" {
		if err := validSSHBrowsePath(file); err != nil {
			return nil, err
		}
	}
	if strings.ContainsAny(ref, "\x00\r\n") || ref != "" && ref != "HEAD" && validCommitHash(ref) != nil && !strings.HasPrefix(ref, "refs/") {
		return nil, errors.New("无效的分支引用")
	}
	process, cancel := context.WithCancel(ctx)
	defer cancel()
	writer := newHistoryLogWriter(ctx, query, skip, cancel)
	writer.limit, writer.filter = limit, filter
	args, err := historyArguments(ctx, repo.read, file, ref, filter, writer)
	if err != nil || args == nil {
		return nil, err
	}
	err = repo.gitTo(process, "", writer, args...)
	if err != nil && !writer.stopped && writer.err == nil {
		return nil, err
	}
	return writer.commits, writer.finish()
}

func (repo SSHWorkspace) Compare(ctx context.Context, base, target string, mergeBase bool) (Comparison, error) {
	return compareRevisions(ctx, repo.read, sshExitCode, base, target, mergeBase)
}

func (repo SSHWorkspace) ComparisonDiff(ctx context.Context, from, target string, file CommitFile) (Diff, error) {
	for _, hash := range []string{from, target} {
		if err := validCommitHash(hash); err != nil {
			return Diff{}, err
		}
	}
	oldPath := file.Path
	if file.OriginalPath != "" {
		oldPath = file.OriginalPath
	}
	for _, name := range []string{oldPath, file.Path} {
		if err := validSSHBrowsePath(name); err != nil {
			return Diff{}, err
		}
	}
	diff, err := selectedPatchPreview(ctx, repo.gitPreview, comparisonPatchArgs(from, target, file), file)
	if err != nil {
		return Diff{}, err
	}
	return repo.diffWithPreviews(ctx, diff, oldPath, from, file.Path, target)
}

func (repo SSHWorkspace) CommitFiles(ctx context.Context, hash string) ([]CommitFile, error) {
	if err := validCommitHash(hash); err != nil {
		return nil, err
	}
	raw, err := repo.read(ctx, "log", "-1", "--format=", "--no-show-signature", "--no-notes", "--root",
		"--diff-merges=first-parent", "--name-status", "--find-renames", "-z", hash, "--")
	if err != nil {
		return nil, err
	}
	return parseCommitFiles(raw)
}

func (repo SSHWorkspace) FileDiff(ctx context.Context, file File, staged bool, commit string) (Diff, error) {
	oldPath := file.Path
	if file.OriginalPath != "" && (commit != "" || staged && strings.ContainsAny(file.Index, "RC") || !staged && strings.ContainsAny(file.Working, "RC")) {
		oldPath = file.OriginalPath
	}
	for _, name := range []string{file.Path, oldPath} {
		if err := validSSHBrowsePath(name); err != nil {
			return Diff{}, err
		}
	}
	if commit != "" {
		if err := validCommitHash(commit); err != nil {
			return Diff{}, err
		}
		selected := CommitFile{Path: file.Path}
		args := []string{"-c", "core.quotePath=true", "show", "--no-show-signature", "--no-notes", "--format=", "--root",
			"--diff-merges=first-parent", "--patch", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", "--no-relative",
			"--submodule=short", "--output-indicator-new=+", "--output-indicator-old=-", "--output-indicator-context= ",
			"--no-ext-diff", "--no-textconv", "--find-renames", commit, "--", file.Path}
		if oldPath != file.Path {
			selected.OriginalPath = oldPath
			args = append(args, oldPath)
		}
		diff, err := selectedPatchPreview(ctx, repo.gitPreview, args, selected)
		if err != nil {
			return Diff{}, err
		}
		parent, err := repo.read(ctx, "rev-parse", "--verify", "--quiet", commit+"^1")
		if err != nil && !sshExitCode(err, 1) {
			return diff, ctx.Err()
		}
		return repo.diffWithPreviews(ctx, diff, oldPath, strings.TrimSpace(parent), file.Path, commit)
	}
	args := []string{"diff"}
	if staged {
		args = append(args, "--cached")
	} else if file.Working == "?" {
		return repo.untrackedDiff(ctx, file.Path)
	}
	args = append(args, "--no-ext-diff", "--no-textconv", "--no-color")
	if oldPath != file.Path {
		args = append(args, "--find-renames")
	}
	args = append(args, "--")
	if oldPath != file.Path {
		args = append(args, oldPath)
	}
	raw, truncated, err := repo.gitPreview(ctx, append(args, file.Path)...)
	if err != nil {
		return Diff{}, err
	}
	diff := sshPatch(raw)
	diff.Truncated = truncated
	if staged {
		return repo.diffWithPreviews(ctx, diff, oldPath, "HEAD", file.Path, "INDEX")
	}
	return repo.diffWithPreviews(ctx, diff, oldPath, "INDEX", file.Path, "WORKTREE")
}

func (repo SSHWorkspace) untrackedDiff(ctx context.Context, file string) (Diff, error) {
	listed, err := repo.read(ctx, "ls-files", "--others", "--exclude-standard", "-z", "--", file)
	if err != nil {
		return Diff{}, err
	}
	if listed != file+"\x00" {
		return Diff{}, errors.New("文件状态已改变，请刷新")
	}
	source, err := repo.readWorktreeFileContent(ctx, newImageAwareFileContent(file, "WORKTREE"))
	if err != nil {
		return Diff{}, err
	}
	if !source.Present || source.Unsupported {
		return Diff{}, errors.New("此文件类型不支持预览或文件状态已改变，请刷新")
	}
	var diff Diff
	switch {
	case source.Truncated:
		diff.Truncated = true
	case source.Binary:
		diff.Binary, diff.Text = true, "二进制文件不提供文本预览"
	default:
		diff = untrackedPatch(file, []byte(source.Text))
	}
	diff.Old, diff.New = newImageAwareFileContent(file, ""), source
	return diff, nil
}
