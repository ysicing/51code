// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// LargeFiles 只传输大小和路径。提交复查固定树中的 blob，覆盖从命令行暂存和 LFS 过滤后的实际内容。
func (repo SSHWorkspace) LargeFiles(ctx context.Context, paths []string, tree string, threshold int64) ([]FileInspection, error) {
	if tree != "" {
		if err := validCommitHash(tree); err != nil {
			return nil, err
		}
		base, err := repo.head(ctx)
		if err != nil {
			return nil, err
		}
		if base == "" {
			base, err = repo.read(ctx, "hash-object", "-t", "tree", "--stdin")
			if err != nil {
				return nil, err
			}
		}
		names, err := repo.read(ctx, "diff", "--no-ext-diff", "--no-renames", "--name-only", "--diff-filter=ACMT", "-z", strings.TrimSpace(base), tree, "--")
		if err != nil || names == "" {
			return nil, err
		}
		paths = strings.Split(strings.TrimSuffix(names, "\x00"), "\x00")
	}
	if len(paths) == 0 {
		return nil, nil
	}
	var quoted strings.Builder
	for _, name := range paths {
		if err := validSSHBrowsePath(name); err != nil {
			return nil, err
		}
		quoted.WriteString(" " + sshShellWord(name))
	}
	command := `set -e
for name in` + quoted.String() + `; do
    if [ ! -L "./$name" ] && [ -f "./$name" ]; then
        size=$(wc -c < "./$name")
        printf '%s\t%s\000' "$size" "$name"
    fi
done`
	if tree != "" {
		command = "exec " + sshGit + " ls-tree -z --format='%(objectsize)%x09%(path)' " + sshShellWord(tree) + " --" + quoted.String()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	raw := limitedBuffer{cancel: cancel}
	err := repo.script(ctx, command, &raw)
	if raw.overflow {
		return nil, errOutputTooLarge
	}
	if err != nil {
		return nil, err
	}
	var large []FileInspection
	for record := range strings.SplitSeq(strings.TrimSuffix(raw.String(), "\x00"), "\x00") {
		if record == "" {
			continue
		}
		sizeText, name, ok := strings.Cut(record, "\t")
		if sizeText == "-" {
			continue
		}
		size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
		if !ok || err != nil || size < 0 {
			return nil, errors.New("无法读取远端文件大小")
		}
		if size >= threshold {
			large = append(large, FileInspection{Path: name, Size: size})
		}
	}
	return large, nil
}

// 在执行写入的同一 SSH 会话内核对窗口看到的分支和 HEAD，拒绝替其他工作树状态执行确认过的操作。
func (repo SSHWorkspace) writeChecked(ctx context.Context, expected Snapshot, command string, timeout time.Duration) error {
	if !expected.SSHWritable {
		return errors.New("远端工作区只读，请刷新仓库")
	}
	ref := "refs/heads/" + expected.Branch
	if expected.Detached {
		ref = ""
	}
	script := `set -e
export GIT_TERMINAL_PROMPT=0 GIT_MERGE_AUTOEDIT=no
fail() { printf '%s\n' "$1" >&2; exit 1; }
head=$(git rev-parse --verify --quiet HEAD) || head=''
ref=$(git symbolic-ref -q HEAD) || ref=''
[ "$ref" = ` + sshShellWord(ref) + ` ] && [ "$head" = ` + sshShellWord(expected.Head) + ` ] || fail '当前分支或 HEAD 已变化，请刷新后重试'
for marker in MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD rebase-merge rebase-apply sequencer; do
    location=$(git rev-parse --git-path "$marker")
    [ ! -e "$location" ] || fail '请先完成远端仓库中正在进行的 Git 操作'
done
` + command
	return repo.scriptTimeout(ctx, script, io.Discard, timeout)
}

func (repo SSHWorkspace) StageFiles(ctx context.Context, expected Snapshot, paths []string, unstage bool) error {
	if len(paths) == 0 {
		return errors.New("请选择文件")
	}
	raw, err := repo.read(ctx, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	_, files, err := parseStatus(raw)
	if err != nil {
		return err
	}
	changed := make(map[string]File, len(files))
	for _, file := range files {
		changed[file.Path] = file
	}
	var args strings.Builder
	for _, name := range paths {
		file, ok := changed[name]
		if !ok || file.Conflict || unstage && (file.Index == " " || file.Index == "?") || !unstage && file.Working == " " {
			return fmt.Errorf("文件状态已改变，请刷新：%s", name)
		}
		names := []string{name}
		if unstage && file.Index == "R" || !unstage && file.Working == "R" {
			names = append(names, file.OriginalPath)
		}
		for _, entry := range names {
			if err := validSSHBrowsePath(entry); err != nil {
				return err
			}
			args.WriteString(" " + sshShellWord(entry))
		}
	}
	action := "add"
	if unstage {
		action = "restore --staged"
		if expected.Head == "" {
			action = "rm --cached --force --ignore-unmatch"
		}
	}
	return repo.writeChecked(ctx, expected, "exec "+sshGit+" "+action+" --"+args.String(), checkoutTimeout)
}

func (repo SSHWorkspace) PrepareCommit(ctx context.Context, expected Snapshot, scan bool) (SecretScan, error) {
	tree, err := repo.read(ctx, "write-tree")
	if err != nil {
		return SecretScan{}, err
	}
	tree = strings.TrimSpace(tree)
	if err := validCommitHash(tree); err != nil {
		return SecretScan{}, err
	}
	result := SecretScan{Token: tree}
	if !scan {
		return result, nil
	}
	head := expected.Head
	if head == "" {
		raw, err := repo.read(ctx, "hash-object", "-t", "tree", "--stdin")
		if err != nil {
			return SecretScan{}, err
		}
		head = strings.TrimSpace(raw)
	}
	patch, err := repo.read(ctx, stagedSecretDiffArgs(head, tree)...)
	if err != nil {
		return SecretScan{}, err
	}
	result, err = scanSecretPatch(ctx, patch)
	result.Token = tree
	return result, err
}

func (repo SSHWorkspace) CommitStaged(ctx context.Context, expected Snapshot, message, tree string) error {
	if err := validCommitHash(tree); err != nil {
		return err
	}
	if strings.TrimSpace(message) == "" || len(message) > 64<<10 || strings.ContainsRune(message, 0) {
		return errors.New("提交说明为空、过长或含有无效字符")
	}
	// 保留真实索引锁，让 Git 和服务端钩子在副本上提交，其他进程不能在校验后改动真实索引。
	return repo.writeChecked(ctx, expected, `index=$(git rev-parse --git-path index)
lock=$index.lock
(set -C; umask 077; : > "$lock") 2>/dev/null || fail '暂存区正在使用，请稍后重试'
trap 'rm -f -- "$lock" "$lock.lock"' EXIT
trap 'exit 1' HUP INT TERM
[ "$(git symbolic-ref -q HEAD || :)" = "$ref" ] && [ "$(git rev-parse --verify --quiet HEAD || :)" = "$head" ] || fail '当前分支或 HEAD 已变化，请刷新后重试'
cp -p -- "$index" "$lock"
export GIT_INDEX_FILE="$lock"
[ "$(git write-tree)" = `+sshShellWord(tree)+` ] || fail '暂存区已变化，请重新提交'
printf '%s' `+sshShellWord(message)+` | git commit --file=-
trap '' HUP INT TERM
mv -f -- "$lock" "$index"
trap - EXIT`, 2*time.Minute)
}

func (repo SSHWorkspace) SwitchLocalBranch(ctx context.Context, expected Snapshot, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsRune(branch, 0) {
		return errors.New("无效的分支名称")
	}
	return repo.writeChecked(ctx, expected, `git show-ref --verify --quiet `+sshShellWord("refs/heads/"+branch)+` || fail '目标分支已不存在'
exec git switch --no-guess --no-overwrite-ignore -- `+sshShellWord(branch), checkoutTimeout)
}

func (repo SSHWorkspace) SyncBranch(ctx context.Context, expected Snapshot, push bool) error {
	if expected.Detached || expected.Head == "" {
		return errors.New("请先切换到已有提交的分支")
	}
	script := `remote=$(git for-each-ref '--format=%(upstream:remotename)' -- "$ref")
target=$(git for-each-ref '--format=%(upstream:remoteref)' -- "$ref")
[ "$remote" = ` + sshShellWord(expected.SSHRemote) + ` ] && [ "$target" = ` + sshShellWord(expected.SSHRemoteRef) + ` ] || fail '分支上游已变化，请刷新后重试'
`
	if push {
		script += `if [ -z "$remote" ] && [ -z "$target" ]; then remote=origin; target=$ref; publish=--set-upstream; else publish=''; fi
case "$target" in refs/heads/*) ;; *) fail '上游不是分支' ;; esac
if [ "$remote" != . ]; then git remote get-url --push -- "$remote" >/dev/null || fail '请先配置推送远程仓库'; fi
mirror=$(git config --bool --get "remote.$remote.mirror") || [ "$?" = 1 ]
[ "$mirror" != true ] || fail '镜像远程不支持普通分支推送'
exec git -c push.recurseSubmodules=no push --no-follow-tags $publish -- "$remote" "$ref:$target"`
	} else {
		script += `test -n "$remote" && test -n "$target" || fail '请先为当前分支设置上游'
if [ "$remote" != . ]; then git fetch -- "$remote"; fi
tip=$(git rev-parse --verify '@{upstream}^{commit}')
git merge-base --is-ancestor "$tip" "$head" && exit 0
git merge-base --is-ancestor "$head" "$tip" || fail '分支已经分叉，无法快进拉取'
[ "$(git symbolic-ref -q HEAD)" = "$ref" ] && [ "$(git rev-parse HEAD)" = "$head" ] || fail '拉取期间当前分支已变化'
git read-tree -n -m -u "$head" "$tip"
exec git -c "branch.${ref#refs/heads/}.mergeOptions=" merge --ff-only --no-edit --no-autostash --no-overwrite-ignore --no-squash -- "$tip"`
	}
	return repo.writeChecked(ctx, expected, script, remoteTimeout)
}
