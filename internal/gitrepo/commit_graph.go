// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// EnsureCommitGraph 在仓库还没有 commit-graph 时写入一次，返回是否实际写入。
// commit-graph 只是 objects/info 下的查询缓存，不改变提交、引用或工作区；历史分页约快 3–4 倍，
// 路径 Bloom 过滤器让单文件历史再快数倍。已有文件时不重复写，后续增量交给 fetch 与 gc。
// 不持有 mu：写入可能耗时较长，不能阻塞界面上的其他 Git 操作，并发写由 Git 自身的锁文件互斥。
func (repo *Repository) EnsureCommitGraph(ctx context.Context) (bool, error) {
	output, err := repo.git(ctx, "rev-parse", "--is-shallow-repository", "--git-path", "objects/info")
	if err != nil {
		return false, err
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != 2 || lines[1] == "" {
		return false, errors.New("无法读取对象目录")
	}
	// 浅克隆中 Git 不使用 commit-graph，写入命令也会成功但不产生文件，跳过以免每次打开都重试。
	if lines[0] == "true" {
		return false, nil
	}
	info := lines[1]
	if !filepath.IsAbs(info) {
		info = filepath.Join(repo.Path, info)
	}
	for _, name := range []string{"commit-graph", filepath.Join("commit-graphs", "commit-graph-chain")} {
		if _, err := os.Stat(filepath.Join(info, name)); err == nil {
			return false, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	// 尊重用户显式关闭 commit-graph 的配置。
	enabled, err := repo.git(ctx, "config", "--type=bool", "--get", "core.commitGraph")
	if err != nil && !exitCode(err, 1) {
		return false, err
	}
	if strings.TrimSpace(enabled) == "false" {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	err = writeCommitGraph(repo.Path)
	if err != nil && removeStaleCommitGraphLocks(info, err) {
		err = writeCommitGraph(repo.Path)
	}
	return err == nil, err
}

func writeCommitGraph(root string) error {
	err := runDetached(root, "commit-graph", "write", "--reachable", "--changed-paths", "--no-progress")
	// Git 2.27 之前不支持 --changed-paths，退回只写提交图。
	if err != nil && strings.Contains(err.Error(), "changed-paths") {
		err = runDetached(root, "commit-graph", "write", "--reachable", "--no-progress")
	}
	return err
}

// staleCommitGraphLockAge 远大于正常持锁时间：Git 先在内存中算完，只在最后写出文件时持锁，通常为毫秒级。
const staleCommitGraphLockAge = time.Hour

// removeStaleCommitGraphLocks 只在写入因 commit-graph 锁已存在而失败时，删除超过阈值的残留锁，返回是否删除。
// 仅限 commit-graph 的两把锁：它们保护的是可重建的查询缓存，即使误删正在写入者的锁，
// 最坏只是对方写入失败或两份完整文件先后覆盖；index.lock 等保护仓库状态的锁不能按此处理。
func removeStaleCommitGraphLocks(info string, failure error) bool {
	removed := false
	for _, lock := range []string{
		filepath.Join(info, "commit-graph.lock"),
		filepath.Join(info, "commit-graphs", "commit-graph-chain.lock"),
	} {
		if !strings.Contains(failure.Error(), filepath.Base(lock)+"': File exists") {
			continue
		}
		stat, err := os.Lstat(lock)
		if err != nil || !stat.Mode().IsRegular() || time.Since(stat.ModTime()) < staleCommitGraphLockAge {
			continue
		}
		if os.Remove(lock) == nil {
			removed = true
		}
	}
	return removed
}

// runDetached 运行一旦开始就必须完整结束的写操作。commit-graph 写入持有锁文件，
// 被 SIGKILL 或 TerminateJobObject 中断会留下 *.lock，之后 gc、fetch 都无法再更新；
// 因此不接受取消，也不放进随应用退出被终止的进程组或 Job，调用方须在启动前完成取消与负载判断。
func runDetached(root string, args ...string) error {
	options := []string{"--no-pager", "--literal-pathspecs", "-C", root, "-c", "color.ui=false"}
	cmd, err := gitCommand(nil, append(options, args...)...)
	if err != nil {
		return err
	}
	cmd.SysProcAttr = detachedProcessAttributes()
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		code := -1
		var failure *exec.ExitError
		if errors.As(err, &failure) {
			code = failure.ExitCode()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return &commandError{code: code, message: message}
	}
	return nil
}
