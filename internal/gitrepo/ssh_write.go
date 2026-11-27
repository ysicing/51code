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

// 按 shell 转义后的长度分批，限制每条 Git 命令的参数开销，同时复用同一 SSH 脚本。
func sshPathBatches(paths []string) ([]string, error) {
	const maxBatchSize = 32 << 10
	var batches []string
	var args strings.Builder
	for _, name := range paths {
		if err := validSSHBrowsePath(name); err != nil {
			return nil, err
		}
		word := sshShellWord(name)
		if args.Len() > 0 && args.Len()+1+len(word) > maxBatchSize {
			batches = append(batches, args.String())
			args.Reset()
		}
		args.WriteByte(' ')
		args.WriteString(word)
	}
	if args.Len() > 0 {
		batches = append(batches, args.String())
	}
	return batches, nil
}

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
	batches, err := sshPathBatches(paths)
	if err != nil {
		return nil, err
	}
	var command strings.Builder
	command.WriteString("set -e\n")
	for _, args := range batches {
		if tree != "" {
			command.WriteString(sshGit + " ls-tree -l -z " + sshShellWord(tree) + " --" + args + "\n")
			continue
		}
		command.WriteString(`for name in` + args + `; do
    if [ ! -L "./$name" ] && [ -f "./$name" ]; then
        size=$(wc -c < "./$name")
        printf '%s\t%s\000' "$size" "$name"
    fi
done
`)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	raw := limitedBuffer{cancel: cancel}
	err = repo.script(ctx, command.String(), &raw)
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
		if !ok {
			return nil, errors.New("无法读取远端文件大小")
		}
		if tree != "" {
			fields := strings.Fields(sizeText)
			if len(fields) != 4 {
				return nil, errors.New("无法读取远端文件大小")
			}
			if fields[1] != "blob" {
				continue
			}
			sizeText = fields[3]
		}
		if sizeText == "-" {
			continue
		}
		size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
		if err != nil || size < 0 {
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
export GIT_TERMINAL_PROMPT=0 GIT_MERGE_AUTOEDIT=no GIT_EDITOR=true GCM_INTERACTIVE=never
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
	var entries []string
	seen := make(map[string]bool, len(paths))
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
			if !seen[entry] {
				seen[entry] = true
				entries = append(entries, entry)
			}
		}
	}
	batches, err := sshPathBatches(entries)
	if err != nil {
		return err
	}
	action := "add"
	if unstage {
		action = "restore --staged"
		if expected.Head == "" {
			action = "rm --cached --force -r --ignore-unmatch"
		}
	}
	var command strings.Builder
	command.WriteString("{\n")
	for _, args := range batches {
		command.WriteString("printf '%s\\000'" + args + "\n")
	}
	command.WriteString("} | " + sshGit + " " + action + " --pathspec-from-file=- --pathspec-file-nul")
	return repo.writeChecked(ctx, expected, command.String(), checkoutTimeout)
}

func (repo SSHWorkspace) PrepareCommit(ctx context.Context, expected Snapshot, scan bool) (SecretScan, error) {
	head := expected.Head
	if head != "" {
		if err := validCommitHash(head); err != nil {
			return SecretScan{}, err
		}
	}
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
guard=''
cleanup_hooks() {
    if [ -n "$guard" ]; then rm -f -- "$guard"/*; rmdir -- "$guard"; fi
}
trap 'rm -f -- "$lock" "$lock.lock"; cleanup_hooks' EXIT
trap 'exit 1' HUP INT TERM
[ "$(git symbolic-ref -q HEAD || :)" = "$ref" ] && [ "$(git rev-parse --verify --quiet HEAD || :)" = "$head" ] || fail '当前分支或 HEAD 已变化，请刷新后重试'
cp -p -- "$index" "$lock"
export GIT_INDEX_FILE="$lock"
[ "$(git write-tree)" = `+sshShellWord(tree)+` ] || fail '暂存区已变化，请重新提交'

export CODE51_ORIGINAL_HOOKS="$(git rev-parse --path-format=absolute --git-path hooks)"
case "$CODE51_ORIGINAL_HOOKS" in /*) ;; *) fail '远端 Git 版本不支持核对最终提交，请升级 Git';; esac
export CODE51_ORIGINAL_GIT_CONFIG="${GIT_CONFIG_PARAMETERS-}"
export CODE51_REVIEWED_TREE=`+sshShellWord(tree)+` CODE51_COMMIT_REF="${ref:-HEAD}"
guard=$(mktemp -d "$(git rev-parse --absolute-git-dir)/51code-hooks.XXXXXX")
cat > "$guard/reference-transaction" <<'51CODE_HOOK'
`+sshCommitHook+`
51CODE_HOOK
chmod +x "$guard/reference-transaction"
for hook in "$CODE51_ORIGINAL_HOOKS"/*; do
    name=${hook##*/}
    if [ "$name" != reference-transaction ] && [ -f "$hook" ] && [ -x "$hook" ]; then
        ln "$guard/reference-transaction" "$guard/$name"
    fi
done
printf '%s' `+sshShellWord(message)+` | git -c color.ui=false -c core.hooksPath="$guard" commit --file=-
trap '' HUP INT TERM
mv -f -- "$lock" "$index"
trap - EXIT
cleanup_hooks`, 2*time.Minute)
}

// Git 把将要更新引用的提交对象交给 prepared 钩子，此时提交钩子已运行，分支尚未更新。
const sshCommitHook = `#!/bin/sh
set -e
name=${0##*/}
original=$CODE51_ORIGINAL_HOOKS/$name
export GIT_CONFIG_PARAMETERS="$CODE51_ORIGINAL_GIT_CONFIG"
if [ "$name" = reference-transaction ] && [ "$1" = prepared ]; then
    updates=$(cat)
    while read -r old next ref; do
        if [ "$ref" = "$CODE51_COMMIT_REF" ]; then
            tree=$(git rev-parse --verify "$next^{tree}")
            if [ "$tree" != "$CODE51_REVIEWED_TREE" ]; then
                printf '%s\n' '提交钩子修改了已检查的暂存内容，请重新暂存并提交' >&2
                exit 1
            fi
        fi
    done <<51CODE_UPDATES
$updates
51CODE_UPDATES
    if [ -x "$original" ]; then
        printf '%s\n' "$updates" | "$original" "$@"
    fi
elif [ -x "$original" ]; then
    exec "$original" "$@"
fi`

func (repo SSHWorkspace) SwitchLocalBranch(ctx context.Context, expected Snapshot, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsRune(branch, 0) {
		return errors.New("无效的分支名称")
	}
	return repo.writeChecked(ctx, expected, `git show-ref --verify --quiet `+sshShellWord("refs/heads/"+branch)+` || fail '目标分支已不存在'
exec git -c color.ui=false switch --no-guess --no-overwrite-ignore -- `+sshShellWord(branch), checkoutTimeout)
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
exec git -c color.ui=false -c push.recurseSubmodules=no push --no-follow-tags $publish -- "$remote" "$ref:$target"`
	} else {
		script += `test -n "$remote" && test -n "$target" || fail '请先为当前分支设置上游'
if [ "$remote" != . ]; then git -c color.ui=false fetch -- "$remote" </dev/null; fi
tip=$(git rev-parse --verify '@{upstream}^{commit}')
git merge-base --is-ancestor "$tip" "$head" && exit 0
git merge-base --is-ancestor "$head" "$tip" || fail '分支已经分叉，无法快进拉取'
[ "$(git symbolic-ref -q HEAD)" = "$ref" ] && [ "$(git rev-parse HEAD)" = "$head" ] || fail '拉取期间当前分支已变化'
git read-tree -n -m -u "$head" "$tip"
GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0="branch.${ref#refs/heads/}.mergeOptions" GIT_CONFIG_VALUE_0="" exec git -c color.ui=false merge --ff-only --no-edit --no-autostash --no-overwrite-ignore --no-squash -- "$tip"`
	}
	return repo.writeChecked(ctx, expected, script, remoteTimeout)
}
