// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// LFSStatus 的 Installed 表示仓库有效 filters 和 hooks 已配置；工具不可用由 Problem 单独报告。
type LFSStatus struct {
	Installed bool
	Files     []LFSFile
	HooksPath string
	Problem   string
}

type LFSFile struct {
	Path       string
	Downloaded bool
}

var lfsExecutableOptions atomic.Pointer[ExecutableOptions]

// ConfigureLFSExecutable 只选择工具，不安装、卸载或禁用任何仓库的 filters/hooks。
func ConfigureLFSExecutable(options ExecutableOptions) {
	lfsExecutableOptions.Store(&options)
}

func currentLFSExecutableOptions() ExecutableOptions {
	if options := lfsExecutableOptions.Load(); options != nil {
		return *options
	}
	return currentExecutableOptionsForLFS()
}

func currentExecutableOptionsForLFS() ExecutableOptions {
	options := currentExecutableOptions()
	if options.Source == "bundled" || options.Source == "" && runtime.GOOS == "windows" {
		return ExecutableOptions{Source: "bundled", Resources: options.Resources}
	}
	return ExecutableOptions{Source: "system"}
}

func ResolveLFSExecutable(options ExecutableOptions) (string, error) {
	return resolveLFSExecutable(options, runtime.GOOS)
}

func resolveLFSExecutable(options ExecutableOptions, platform string) (string, error) {
	source := options.Source
	if source == "" {
		source = "system"
		if platform == "windows" {
			source = "bundled"
		}
	}
	var path string
	switch source {
	case "system":
		var err error
		path, err = exec.LookPath("git-lfs")
		if err != nil {
			return "", errors.New("未找到系统 Git LFS，请选择可用的 Git LFS 可执行文件")
		}
	case "bundled":
		if platform != "windows" || options.Resources == "" {
			return "", errors.New("当前安装不包含内置 Git LFS，请选择系统或自定义 Git LFS")
		}
		path = filepath.Join(options.Resources, "git", "cmd", "git-lfs.exe")
	case "custom":
		path = options.Path
		if !filepath.IsAbs(path) {
			return "", errors.New("请选择 Git LFS 可执行文件的绝对路径")
		}
	default:
		return "", errors.New("无效的 Git LFS 来源")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// hooks 和 Git filters 按固定名称寻找程序，不能接受一个只能直接启动的别名。
	name := filepath.Base(path)
	if platform == "windows" {
		if !strings.EqualFold(name, "git-lfs.exe") {
			return "", errors.New("Git LFS 可执行文件必须命名为 git-lfs.exe")
		}
	} else if name != "git-lfs" {
		return "", errors.New("Git LFS 可执行文件必须命名为 git-lfs")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("未找到所选 Git LFS 可执行文件，请重新选择；不会回退到其他版本")
	}
	if platform != "windows" && info.Mode().Perm()&0111 == 0 {
		return "", errors.New("所选 Git LFS 文件没有执行权限")
	}
	if platform != "windows" {
		// Homebrew 的 bin 入口是符号链接；真实目录独立于 Git，避免错误判定工具冲突。
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		if filepath.Base(path) != "git-lfs" {
			return "", errors.New("Git LFS 的真实可执行文件必须命名为 git-lfs，供 hooks 和 filters 调用")
		}
	}
	if strings.ContainsRune(filepath.Dir(path), os.PathListSeparator) {
		return "", errors.New("Git LFS 目录不能包含 PATH 列表分隔符")
	}
	return path, nil
}

type lfsGitExecPath struct {
	gitPath  string
	execPath string
}

var lfsGitExecPathCache atomic.Pointer[lfsGitExecPath]

func lfsEnvironment(ctx context.Context, gitPath, lfsPath string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var err error
	gitPath, err = filepath.EvalSymlinks(gitPath)
	if err != nil {
		return nil, err
	}
	lfsDirectory := filepath.Dir(lfsPath)
	gitName := "git"
	if runtime.GOOS == "windows" {
		gitName += ".exe"
	}
	// Git 会把 exec-path 加到 PATH 最前面；拒绝会反过来替换所选 Git 的工具目录。
	if other, err := os.Stat(filepath.Join(lfsDirectory, gitName)); err == nil {
		selected, err := os.Stat(gitPath)
		if err != nil {
			return nil, err
		}
		if !os.SameFile(selected, other) {
			return nil, errors.New("Git LFS 目录包含另一 Git，请选择与所选 Git 配套的 Git LFS，或将 Git LFS 放在独立目录")
		}
	}
	cached := lfsGitExecPathCache.Load()
	if cached == nil || cached.gitPath != gitPath {
		probeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		command := exec.CommandContext(probeContext, gitPath, "--exec-path")
		command.Env = gitEnvironment()
		output, err := runLFSProcess(probeContext, command)
		if err != nil {
			return nil, err
		}
		path := strings.TrimSuffix(output, "\n")
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
			return nil, errors.New("无法确定所选 Git 的辅助程序目录")
		}
		cached = &lfsGitExecPath{gitPath: gitPath, execPath: path}
		lfsGitExecPathCache.Store(cached)
	}
	selectedLFS, err := os.Stat(lfsPath)
	if err != nil {
		return nil, err
	}
	lfsNames := []string{"git-lfs"}
	if runtime.GOOS == "windows" {
		lfsNames = append([]string{"git-lfs.exe"}, lfsNames...)
	}
	// Git 自己会前置真实 exec-path；不能通过替换它来绕过另一 LFS，否则会破坏用户 hooks。
	for _, name := range lfsNames {
		path := filepath.Join(cached.execPath, name)
		other, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("无法检查 Git exec-path 的 LFS 程序：%w", err)
		}
		if !os.SameFile(selectedLFS, other) {
			return nil, fmt.Errorf("Git exec-path 中的 %s 会优先覆盖所选 Git LFS，不支持此工具组合；请选择配套的 Git LFS", path)
		}
	}
	environment := gitEnvironment()
	directories := append([]string{lfsDirectory}, executableDirectories(gitPath, runtime.GOOS)...)
	directories = append(directories, cached.execPath)
	for index, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "PATH") {
			environment[index] = key + "=" + strings.Join(directories, string(os.PathListSeparator)) + string(os.PathListSeparator) + value
			return environment, nil
		}
	}
	return append(environment, "PATH="+strings.Join(directories, string(os.PathListSeparator))), nil
}

func runLFS(ctx context.Context, root string, options ExecutableOptions, timeout time.Duration, args ...string) (string, error) {
	var stdout limitedBuffer
	if err := runLFSTo(ctx, root, options, timeout, &stdout, stdout.String, args...); err != nil {
		return "", err
	}
	if stdout.overflow {
		return "", errors.New("Git LFS 输出超过 4 MiB，请使用命令行查看完整结果")
	}
	return stdout.String(), nil
}

// runLFSTo 将标准输出直接交给 stdout，供可能超过 4 MiB 的列表流式解析；details 在失败且 stderr 为空时补充说明。
func runLFSTo(ctx context.Context, root string, options ExecutableOptions, timeout time.Duration, stdout io.Writer, details func() string, args ...string) error {
	path, err := ResolveLFSExecutable(options)
	if err != nil {
		return err
	}
	gitPath, err := ResolveExecutable(currentExecutableOptions())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, args...)
	command.Dir = root
	command.Env, err = lfsEnvironment(ctx, gitPath, path)
	if err != nil {
		return err
	}
	return runLFSProcessTo(ctx, command, stdout, details)
}

func runLFSProcess(ctx context.Context, command *exec.Cmd) (string, error) {
	var stdout limitedBuffer
	if err := runLFSProcessTo(ctx, command, &stdout, stdout.String); err != nil {
		return "", err
	}
	if stdout.overflow {
		return "", errors.New("Git LFS 输出超过 4 MiB，请使用命令行查看完整结果")
	}
	return stdout.String(), nil
}

func runLFSProcessTo(ctx context.Context, command *exec.Cmd, stdout io.Writer, details func() string) error {
	command.WaitDelay = 2 * time.Second
	var stderr limitedBuffer
	command.Stdout, command.Stderr = stdout, &stderr
	err := runProcess(ctx, command)
	if ctx.Err() != nil {
		return fmt.Errorf("Git LFS 操作已取消或超时：%w", ctx.Err())
	}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(details())
		}
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("Git LFS 执行失败：%s: %w", message, err)
	}
	if stderr.overflow {
		return errors.New("Git LFS 输出超过 4 MiB，请使用命令行查看完整结果")
	}
	return nil
}

func InspectLFSExecutable(ctx context.Context, options ExecutableOptions) (ExecutableInfo, error) {
	path, err := ResolveLFSExecutable(options)
	if err != nil {
		return ExecutableInfo{}, err
	}
	output, err := runLFS(ctx, "", options, 10*time.Second, "version")
	if err != nil {
		return ExecutableInfo{}, err
	}
	version := strings.TrimSpace(output)
	if !strings.HasPrefix(version, "git-lfs/") || strings.ContainsAny(version, "\r\n") {
		return ExecutableInfo{}, errors.New("所选程序不是有效的 Git LFS")
	}
	return ExecutableInfo{Path: path, Version: version}, nil
}

func (repo *Repository) LFSStatus(ctx context.Context) (LFSStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	state := LFSStatus{Files: []LFSFile{}}
	if err := repo.lockHistory(ctx); err != nil {
		return state, err
	}
	defer repo.mu.Unlock()
	var err error
	state.Installed, state.HooksPath, state.Problem, err = repo.lfsInstallation(ctx)
	if err != nil {
		return state, err
	}
	if _, err := InspectLFSExecutable(ctx, currentLFSExecutableOptions()); err != nil {
		state.Problem = strings.TrimSpace(state.Problem + "\n" + err.Error())
		if ctx.Err() != nil {
			return state, ctx.Err()
		}
		return state, nil
	}
	// 每个文件约 200 字节 JSON，约 2 万个 LFS 文件即超过 4 MiB 输出上限；改为边读边解码。
	reader, writer := io.Pipe()
	type decoded struct {
		files     []LFSFile
		truncated bool
		err       error
	}
	result := make(chan decoded, 1)
	go func() {
		files, truncated, err := decodeLFSFiles(reader, maxLFSStatusFiles)
		// 解码提前结束（出错或达到上限）时继续排空，Git LFS 不会阻塞在满管道上。
		_, _ = io.Copy(io.Discard, reader)
		result <- decoded{files, truncated, err}
	}()
	err = runLFSTo(ctx, repo.Path, currentLFSExecutableOptions(), 30*time.Second, writer, func() string { return "" }, "ls-files", "--json")
	_ = writer.CloseWithError(err)
	files := <-result
	if err != nil {
		state.Problem = err.Error()
		return state, err
	}
	if files.err != nil {
		return state, files.err
	}
	state.Files = files.files
	if files.truncated {
		state.Problem = strings.TrimSpace(state.Problem + "\n" + fmt.Sprintf("LFS 文件过多，仅列出前 %d 个，请使用命令行查看完整列表", maxLFSStatusFiles))
	}
	return state, nil
}

// maxLFSStatusFiles 限制界面列表的条目数，避免超大仓库一次性构建过长列表。
const maxLFSStatusFiles = 100000

// decodeLFSFiles 流式解析 git lfs ls-files --json 的 {"files":[...]}，最多保留 limit 条。
func decodeLFSFiles(reader io.Reader, limit int) ([]LFSFile, bool, error) {
	files := []LFSFile{}
	invalid := func(err error) ([]LFSFile, bool, error) {
		return nil, false, fmt.Errorf("无法解析 Git LFS 文件状态：%w", err)
	}
	decoder := json.NewDecoder(reader)
	if token, err := decoder.Token(); err != nil {
		return invalid(err)
	} else if token != json.Delim('{') {
		return invalid(errors.New("顶层不是对象"))
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return invalid(err)
		}
		if token != "files" {
			var skipped json.RawMessage
			if err := decoder.Decode(&skipped); err != nil {
				return invalid(err)
			}
			continue
		}
		// 无文件时 Git LFS 可能输出 null。
		if token, err = decoder.Token(); err != nil {
			return invalid(err)
		} else if token == nil {
			continue
		} else if token != json.Delim('[') {
			return invalid(errors.New("files 不是数组"))
		}
		for decoder.More() {
			if len(files) >= limit {
				return files, true, nil
			}
			var file struct {
				Name       string `json:"name"`
				Downloaded bool   `json:"downloaded"`
			}
			if err := decoder.Decode(&file); err != nil {
				return invalid(err)
			}
			name := file.Name
			if len(name) >= 2 && strings.HasPrefix(name, "\"") && strings.HasSuffix(name, "\"") {
				var err error
				name, err = strconv.Unquote(name)
				if err != nil {
					return nil, false, fmt.Errorf("无法解析 Git LFS 的 C-quoted 文件名：%w", err)
				}
			}
			if err := validPath(name); err != nil {
				return nil, false, fmt.Errorf("Git LFS 返回了不安全的文件路径：%w", err)
			}
			files = append(files, LFSFile{Path: name, Downloaded: file.Downloaded})
		}
		if _, err := decoder.Token(); err != nil {
			return invalid(err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return invalid(err)
	}
	return files, false, nil
}

func (repo *Repository) lfsInstallation(ctx context.Context) (bool, string, string, error) {
	directory, err := repo.lfsHooksPath(ctx)
	if err != nil {
		return false, "", "", err
	}
	missing, hookErr := preflightLFSHooks(directory)
	output, err := repo.git(ctx, "config", "--null", "--get-regexp", `^filter\.lfs\.(clean|smudge|process|required)$`)
	if err != nil && !exitCode(err, 1) {
		return false, directory, "", err
	}
	filters := map[string]string{}
	for _, record := range strings.Split(output, "\x00") {
		if key, value, ok := strings.Cut(record, "\n"); ok {
			filters[key] = strings.TrimSpace(value)
		}
	}
	clean := filters["filter.lfs.clean"] == "git-lfs clean -- %f"
	smudge := filters["filter.lfs.smudge"] == "git-lfs smudge -- %f" ||
		filters["filter.lfs.smudge"] == "git-lfs smudge --skip -- %f"
	process := filters["filter.lfs.process"] == "" || filters["filter.lfs.process"] == "git-lfs filter-process" ||
		filters["filter.lfs.process"] == "git-lfs filter-process --skip"
	required := slices.Contains([]string{"true", "yes", "on", "1"}, strings.ToLower(filters["filter.lfs.required"]))
	if hookErr != nil {
		return false, directory, hookErr.Error(), nil
	}
	if !clean || !smudge || !process || !required {
		return false, directory, "当前仓库尚未配置兼容的 Git LFS filters，请先初始化", nil
	}
	if len(missing) != 0 {
		return false, directory, "当前仓库缺少 Git LFS hooks：" + strings.Join(missing, "、"), nil
	}
	return true, directory, "", nil
}

var lfsHookNames = []string{"pre-push", "post-checkout", "post-commit", "post-merge"}

func lfsHookContent(name string) string {
	return "#!/bin/sh\ngit lfs " + name + " \"$@\"\n"
}

func knownLFSHook(content, name string) bool {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if content == strings.TrimSpace(lfsHookContent(name)) {
		return true
	}
	standard := "#!/bin/sh\ncommand -v git-lfs >/dev/null 2>&1 || { printf >&2 \"\\n%s\\n\\n\" \"This repository is configured for Git LFS but 'git-lfs' was not found on your path. If you no longer wish to use Git LFS, remove this hook by deleting the '" + name + "' file in the hooks directory (set by 'core.hookspath'; usually '.git/hooks').\"; exit 2; }\ngit lfs " + name + " \"$@\""
	return content == standard
}

func (repo *Repository) lfsHooksPath(ctx context.Context) (string, error) {
	output, err := repo.git(ctx, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	path := strings.TrimSuffix(output, "\n")
	if !filepath.IsAbs(path) {
		return "", errors.New("无法确定 Git hooks 的绝对路径")
	}
	return path, nil
}

func preflightLFSHooks(directory string) ([]string, error) {
	var missing []string
	for _, name := range lfsHookNames {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			missing = append(missing, name)
			continue
		}
		if err != nil {
			return nil, err
		}
		conflict := fmt.Errorf("Git LFS hook 冲突：%s；请手动整合现有 hook，应用不会覆盖它", path)
		if !info.Mode().IsRegular() || info.Size() > 16<<10 || runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
			return nil, conflict
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 16<<10+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.Join(readErr, closeErr)
		}
		if !knownLFSHook(string(data), name) {
			return nil, conflict
		}
	}
	return missing, nil
}

func (repo *Repository) InitializeLFS(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := repo.lockHistory(ctx); err != nil {
		return err
	}
	defer repo.mu.Unlock()
	if _, err := InspectLFSExecutable(ctx, currentLFSExecutableOptions()); err != nil {
		return err
	}
	directory, err := repo.lfsHooksPath(ctx)
	if err != nil {
		return err
	}
	missing, err := preflightLFSHooks(directory)
	if err != nil {
		return err
	}
	if len(missing) != 0 {
		output, err := repo.git(ctx, "config", "--show-scope", "--get", "core.hooksPath")
		if err != nil && !exitCode(err, 1) {
			return err
		}
		if err == nil {
			fields := strings.Fields(output)
			if len(fields) == 0 {
				return errors.New("无法确定 core.hooksPath 的配置作用域，请手动检查 hooks 配置")
			}
			if fields[0] == "global" || fields[0] == "system" {
				return fmt.Errorf("core.hooksPath 来自 %s 共享配置（%s），缺少 LFS hooks：%s；请手动整合共享 hooks，或为当前仓库显式配置独立的本地 hooks 目录后重试",
					fields[0], directory, strings.Join(missing, "、"))
			}
		}
	}
	// install 即使不带 --force 也会升级既有标准 hook；跳过它，独占创建缺少的文件。
	if _, err := runLFS(ctx, repo.Path, currentLFSExecutableOptions(), 30*time.Second, "install", "--local", "--skip-repo"); err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	for _, name := range missing {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
		if err != nil {
			return fmt.Errorf("无法安全创建 LFS hook %s（未覆盖已有文件）：%w", name, err)
		}
		_, writeErr := io.WriteString(file, lfsHookContent(name))
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return err
		}
	}
	return nil
}

func (repo *Repository) PullLFS(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := repo.lockHistory(ctx); err != nil {
		return err
	}
	defer repo.mu.Unlock()
	_, err := runLFS(ctx, repo.Path, currentLFSExecutableOptions(), 10*time.Minute, "pull")
	return err
}

func lfsPaths(paths []string) ([]string, error) {
	if len(paths) == 0 || len(paths) > 1024 {
		return nil, errors.New("请选择 1 至 1024 个文件")
	}
	var result []string
	for _, path := range paths {
		if err := validPath(path); err != nil {
			return nil, err
		}
		if !utf8.ValidString(path) || len(path) > 4096 ||
			filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) != path ||
			strings.ContainsAny(path, "\\:") {
			return nil, errors.New("LFS 需要规范的仓库相对文件路径")
		}
		for _, character := range path {
			if character < 32 || character == 127 {
				return nil, errors.New("LFS 文件名不能含控制字符")
			}
		}
		if strings.EqualFold(filepath.Base(path), ".gitattributes") {
			return nil, errors.New("不能将 .gitattributes 作为 LFS 文件，请单独处理其暂存")
		}
		if runtime.GOOS == "windows" {
			for _, part := range strings.Split(path, "/") {
				if strings.TrimRight(part, " .") != part {
					return nil, errors.New("Windows 文件路径不能以空格或点结尾")
				}
			}
		}
		if !slices.Contains(result, path) {
			result = append(result, path)
		}
	}
	return result, nil
}

func lfsRule(path string, tracked bool) string {
	var pattern strings.Builder
	pattern.WriteByte('/')
	for _, character := range path {
		if strings.ContainsRune("*?[]\\", character) {
			pattern.WriteByte('\\')
		}
		pattern.WriteRune(character)
	}
	escaped := strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(pattern.String())
	attributes := " filter=lfs diff=lfs merge=lfs -text\n"
	if !tracked {
		// 精确覆盖该文件的通配规则，保留其他文件及用户原有规则。
		attributes = " !filter !diff !merge !text\n"
	}
	return "\"" + escaped + "\"" + attributes
}

func appendLFSRules(content, rules string) string {
	if strings.HasSuffix(content, rules) {
		return content
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + rules
}

func validateLFSAttributes(content string) error {
	invalid := errors.New(".gitattributes 格式无效或含冲突标记，请先手动修复")
	if len(content) > fileContentLimit || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return invalid
	}
	for _, line := range strings.Split(content, "\n") {
		for _, character := range line {
			if character < 32 && character != '\t' && character != '\r' || character == 127 {
				return invalid
			}
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "<<<<<<<") || strings.HasPrefix(line, "=======") || strings.HasPrefix(line, ">>>>>>>") {
			return invalid
		}
		if strings.HasPrefix(line, "\"") {
			end := -1
			for index := 1; index < len(line); index++ {
				if line[index] == '\\' {
					index++
					if index >= len(line) {
						return invalid
					}
					if line[index] >= '0' && line[index] <= '3' {
						if index+2 >= len(line) || line[index+1] < '0' || line[index+1] > '7' || line[index+2] < '0' || line[index+2] > '7' {
							return invalid
						}
						index += 2
					} else if !strings.ContainsRune("abtnvfr\\\"", rune(line[index])) {
						return invalid
					}
				} else if line[index] == '"' {
					end = index
					break
				}
			}
			if end < 0 || end+1 == len(line) || line[end+1] != ' ' && line[end+1] != '\t' {
				return invalid
			}
			line = strings.TrimSpace(line[end+1:])
		} else {
			position := strings.IndexAny(line, " \t")
			if position < 0 || strings.HasPrefix(line, "!") {
				return invalid
			}
			line = strings.TrimSpace(line[position:])
		}
		if line == "" {
			return invalid
		}
		for _, attribute := range strings.Fields(line) {
			name := attribute
			if strings.HasPrefix(name, "-") || strings.HasPrefix(name, "!") {
				name = name[1:]
			}
			name, _, _ = strings.Cut(name, "=")
			if name == "" || strings.HasPrefix(name, "-") {
				return invalid
			}
			for _, character := range name {
				if character != '-' && character != '_' && character != '.' &&
					(character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
					(character < '0' || character > '9') {
					return invalid
				}
			}
		}
	}
	return nil
}

type lfsAttributes struct {
	content string
	info    os.FileInfo
}

func readLFSAttributes(root *os.Root) (lfsAttributes, error) {
	state := lfsAttributes{}
	info, err := root.Lstat(".gitattributes")
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	file, err := openFileContent(root, ".gitattributes", info)
	if err != nil {
		return state, fmt.Errorf("无法安全读取 .gitattributes：%w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, fileContentLimit+1))
	if err != nil {
		return state, err
	}
	state.content, state.info = string(content), info
	return state, validateLFSAttributes(state.content)
}

func checkLFSFiles(root *os.Root, paths []string, requirePresent bool) error {
	for _, path := range paths {
		parent := filepath.ToSlash(filepath.Dir(path))
		for parent != "." {
			info, err := root.Lstat(parent)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("LFS 文件的父目录不安全或不存在：%s", parent)
			}
			if _, err := root.Lstat(parent + "/.git"); err == nil || !os.IsNotExist(err) {
				return fmt.Errorf("LFS 文件不能位于嵌套仓库中：%s", path)
			}
			parent = filepath.ToSlash(filepath.Dir(parent))
		}
		info, err := root.Lstat(path)
		if os.IsNotExist(err) && !requirePresent {
			continue
		}
		if err != nil {
			return err
		}
		file, err := openFileContent(root, path, info)
		if err != nil {
			return fmt.Errorf("LFS 只接受普通文件：%s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

func appendLFSAttributes(root *os.Root, before lfsAttributes, after string) (lfsAttributes, error) {
	current, err := readLFSAttributes(root)
	if err != nil {
		return lfsAttributes{}, err
	}
	if current.content != before.content || (current.info == nil) != (before.info == nil) ||
		current.info != nil && !os.SameFile(current.info, before.info) {
		return lfsAttributes{}, errors.New(".gitattributes 已被外部修改，请重试")
	}
	if after == before.content {
		return current, nil
	}
	var file *os.File
	if before.info == nil {
		file, err = root.OpenFile(".gitattributes", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	} else {
		file, err = openLFSAttributesAppend(root)
	}
	if err != nil {
		return lfsAttributes{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return lfsAttributes{}, err
	}
	if !opened.Mode().IsRegular() || before.info != nil && !os.SameFile(opened, before.info) {
		return lfsAttributes{}, errors.New(".gitattributes 已被替换，请重试")
	}
	_, writeErr := io.WriteString(file, strings.TrimPrefix(after, before.content))
	writeErr = errors.Join(writeErr, file.Sync())
	if writeErr != nil {
		return lfsAttributes{}, errors.Join(writeErr, errors.New(".gitattributes 追加失败，已保留工作区内容，请检查本次规则"))
	}
	return lfsAttributes{content: after, info: opened}, nil
}

func (repo *Repository) rollbackLFSAttributes(root *os.Root, before, after lfsAttributes) error {
	if after.content == before.content {
		return nil
	}
	current, err := readLFSAttributes(root)
	if err != nil {
		return err
	}
	if current.content != after.content {
		return errors.New(".gitattributes 已被外部修改，未覆盖用户内容；请手动撤销本次 LFS 规则")
	}
	if current.info == nil || after.info == nil || !os.SameFile(current.info, after.info) {
		return errors.New(".gitattributes 已被替换，无法自动回滚")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := repo.git(ctx, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp(filepath.Dir(strings.TrimSuffix(output, "\n")), ".51code-lfs-attributes-")
	if err != nil {
		return err
	}
	backup := filepath.Join(directory, ".gitattributes")
	// 先移动并保留原 inode；编辑器持有的旧句柄即使继续写入，也不会被截断或删除。
	if err := os.Rename(filepath.Join(root.Name(), ".gitattributes"), backup); err != nil {
		_ = os.Remove(directory)
		return err
	}
	recovery, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("属性原文件保留在 %s：%w", backup, err)
	}
	defer recovery.Close()
	moved, err := readLFSAttributes(recovery)
	if err != nil || moved.content != after.content || moved.info == nil || !os.SameFile(after.info, moved.info) {
		restoreErr := os.Link(backup, filepath.Join(root.Name(), ".gitattributes"))
		return errors.Join(err, restoreErr, fmt.Errorf(".gitattributes 在回滚期间发生变化，原文件保留在 %s", backup))
	}
	if before.info == nil {
		return nil
	}
	restored := filepath.Join(directory, "restored")
	file, err := os.OpenFile(restored, os.O_WRONLY|os.O_CREATE|os.O_EXCL, before.info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("属性原文件保留在 %s：%w", backup, err)
	}
	_, writeErr := io.WriteString(file, before.content)
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err == nil {
		// Link 只在目标不存在时发布完整内容，不能覆盖回滚期间编辑器新保存的文件。
		err = os.Link(restored, filepath.Join(root.Name(), ".gitattributes"))
	}
	_ = os.Remove(restored)
	if err != nil {
		return fmt.Errorf("未覆盖并发保存的属性文件，原文件保留在 %s：%w", backup, err)
	}
	return nil
}

// TrackLFS 追加仅匹配所选文件的规则，不暂存属性文件，不改写已有规则。
func (repo *Repository) TrackLFS(ctx context.Context, paths []string) error {
	return repo.setLFSTracking(ctx, paths, true)
}

// UntrackLFS 精确取消所选文件的属性，不移除仍用于其他文件的通配规则。
func (repo *Repository) UntrackLFS(ctx context.Context, paths []string) error {
	return repo.setLFSTracking(ctx, paths, false)
}

func (repo *Repository) setLFSTracking(ctx context.Context, paths []string, tracked bool) error {
	names, err := lfsPaths(paths)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := repo.lockHistory(ctx); err != nil {
		return err
	}
	defer repo.mu.Unlock()
	if _, err := InspectLFSExecutable(ctx, currentLFSExecutableOptions()); err != nil {
		return err
	}
	root, err := os.OpenRoot(repo.Path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := checkLFSFiles(root, names, false); err != nil {
		return err
	}
	before, err := readLFSAttributes(root)
	if err != nil {
		return err
	}
	var rules strings.Builder
	for _, name := range names {
		rules.WriteString(lfsRule(name, tracked))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = appendLFSAttributes(root, before, appendLFSRules(before.content, rules.String()))
	return err
}

// lfsGit 使用隔离索引执行 Git，仍复用所选 Git、环境清理和进程树取消策略。
func (repo *Repository) lfsGit(ctx context.Context, index, input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	options := []string{"--no-pager", "--literal-pathspecs", "-c", "color.ui=false", "-c", "core.splitIndex=false"}
	command, err := gitCommand(ctx, append(options, args...)...)
	if err != nil {
		return "", err
	}
	command.Dir, command.Stdin = repo.Path, strings.NewReader(input)
	if index != "" {
		command.Env = append(command.Env, "GIT_INDEX_FILE="+index)
	}
	return runLFSProcess(ctx, command)
}

func (repo *Repository) lfsIndexEntries(ctx context.Context, index string, paths []string) (string, error) {
	return repo.lfsGit(ctx, index, "", append([]string{"ls-files", "--stage", "-z", "--"}, paths...)...)
}

func lfsEntriesByPath(entries string) (map[string]string, error) {
	result := make(map[string]string)
	for _, record := range strings.Split(entries, "\x00") {
		if record == "" {
			continue
		}
		metadata, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || fields[2] != "0" || fields[0] != "100644" && fields[0] != "100755" {
			return nil, errors.New("LFS 不支持冲突、子模块或符号链接索引条目")
		}
		if err := validCommitHash(fields[1]); err != nil {
			return nil, err
		}
		result[path] = record + "\x00"
	}
	return result, nil
}

func validLFSPointer(content string) bool {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(lines) != 3 || lines[0] != "version https://git-lfs.github.com/spec/v1" {
		return false
	}
	oid, ok := strings.CutPrefix(lines[1], "oid sha256:")
	if !ok || len(oid) != 64 {
		return false
	}
	if _, err := hex.DecodeString(oid); err != nil {
		return false
	}
	size, ok := strings.CutPrefix(lines[2], "size ")
	if !ok || size == "" {
		return false
	}
	for _, character := range size {
		if character < '0' || character > '9' {
			return false
		}
	}
	_, err := strconv.ParseInt(size, 10, 64)
	return err == nil
}

func (repo *Repository) lfsTemporaryIndex(ctx context.Context) (string, func(), error) {
	output, err := repo.git(ctx, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", nil, err
	}
	path := strings.TrimSuffix(output, "\n")
	file, err := os.CreateTemp(filepath.Dir(path), ".51code-lfs-index-*")
	if err != nil {
		return "", nil, err
	}
	name := file.Name()
	cleanup := func() {
		_ = os.Remove(name)
		_ = os.Remove(name + ".lock")
	}
	source, err := os.Open(path)
	if os.IsNotExist(err) {
		err = file.Close()
		if err == nil {
			err = os.Remove(name)
		}
		if err == nil {
			_, err = repo.lfsGit(ctx, name, "", "read-tree", "--empty")
		}
	} else if err == nil {
		_, copyErr := io.Copy(file, source)
		err = errors.Join(copyErr, source.Close(), file.Close())
	} else {
		_ = file.Close()
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return name, cleanup, nil
}

func lfsExpectedSubset(paths []string, expected []FileInspection) ([]FileInspection, error) {
	known := make(map[string]FileInspection, len(expected))
	for _, file := range expected {
		if _, duplicate := known[file.Path]; duplicate {
			return nil, errors.New("文件检查报告含重复路径，请重新检查")
		}
		known[file.Path] = file
	}
	selected := make([]FileInspection, 0, len(paths))
	for _, path := range paths {
		file, exists := known[path]
		if !exists {
			return nil, fmt.Errorf("文件 %s 未包含在确认报告中，请重新检查", path)
		}
		selected = append(selected, file)
	}
	return selected, nil
}

func sameLFSContent(current, expected []FileInspection) bool {
	if len(current) != len(expected) {
		return false
	}
	known := make(map[string]FileInspection, len(expected))
	for _, file := range expected {
		if _, duplicate := known[file.Path]; duplicate {
			return false
		}
		known[file.Path] = file
	}
	for _, file := range current {
		previous, exists := known[file.Path]
		if !exists || file.Size != previous.Size || file.Fingerprint == "" || file.Fingerprint != previous.Fingerprint {
			return false
		}
		delete(known, file.Path)
	}
	return len(known) == 0
}

func (repo *Repository) verifyLFSInspectionIndex(ctx context.Context, expected *FileInspectionReport) error {
	if expected == nil {
		return nil
	}
	if expected.Token == "" {
		return errors.New("文件检查报告缺少索引凭据，请重新检查")
	}
	token, err := runDigest(ctx, repo.Path, 30*time.Second, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	if token != expected.Token {
		return errors.New("暂存区已变化，请重新检查后再使用 LFS 暂存")
	}
	return nil
}

// StageWithLFS 只合入所选文件与新增规则；工作区和索引中的其他属性保持各自版本。
func (repo *Repository) StageWithLFS(ctx context.Context, paths []string) error {
	return repo.stageWithLFS(ctx, paths, nil)
}

// StageWithLFSInspected 仅转换确认报告中内容和索引仍未变化的所选文件。
func (repo *Repository) StageWithLFSInspected(ctx context.Context, paths []string, expected FileInspectionReport) error {
	expected.Files = slices.Clone(expected.Files)
	return repo.stageWithLFS(ctx, paths, &expected)
}

func (repo *Repository) stageWithLFS(ctx context.Context, paths []string, expected *FileInspectionReport) (result error) {
	names, err := lfsPaths(paths)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := repo.lockHistory(ctx); err != nil {
		return err
	}
	defer repo.mu.Unlock()
	root, err := os.OpenRoot(repo.Path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := checkLFSFiles(root, names, true); err != nil {
		return err
	}
	originalFiles, err := repo.inspectWorktreeFiles(ctx, names)
	if err != nil {
		return err
	}
	if len(originalFiles) != len(names) {
		return errors.New("工作区文件已变化，请重新检查后再使用 LFS 暂存")
	}
	if expected != nil {
		selected, err := lfsExpectedSubset(names, expected.Files)
		if err != nil {
			return err
		}
		if !sameInspectedFiles(originalFiles, selected) {
			return errors.New("工作区文件已变化，请重新检查后再使用 LFS 暂存")
		}
	}
	if err := repo.verifyLFSInspectionIndex(ctx, expected); err != nil {
		return err
	}
	output, err := repo.git(ctx, append([]string{"status", "--porcelain=v2", "--no-renames", "-z", "--untracked-files=all", "--"}, names...)...)
	if err != nil {
		return err
	}
	_, files, err := parseStatus(output)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.Conflict || file.Index != " " && file.Index != "?" && file.Working != " " {
			return fmt.Errorf("文件 %s 已部分暂存或存在冲突，请先处理其暂存内容", file.Path)
		}
	}
	allPaths := append(slices.Clone(names), ".gitattributes")
	original, err := repo.lfsIndexEntries(ctx, "", allPaths)
	if err != nil {
		return err
	}
	originalEntries, err := lfsEntriesByPath(original)
	if err != nil {
		return err
	}
	flags, err := repo.git(ctx, append([]string{"ls-files", "-v", "-z", "--"}, allPaths...)...)
	if err != nil {
		return err
	}
	for _, record := range strings.Split(flags, "\x00") {
		if record != "" && (record[0] == 'S' || record[0] >= 'a' && record[0] <= 'z') {
			return errors.New("请先取消所选文件和 .gitattributes 的 skip-worktree/assume-unchanged 标记")
		}
	}
	before, err := readLFSAttributes(root)
	if err != nil {
		return err
	}
	indexAttributes, err := repo.indexFileContent(ctx, ".gitattributes")
	if err != nil {
		return err
	}
	if indexAttributes.Unsupported || indexAttributes.Binary || indexAttributes.Truncated {
		return errors.New("索引中的 .gitattributes 无法安全修改")
	}
	if err := validateLFSAttributes(indexAttributes.Text); err != nil {
		return err
	}
	if !indexAttributes.Present {
		deleted, err := repo.git(ctx, "diff", "--cached", "--diff-filter=D", "--name-only", "-z", "--", ".gitattributes")
		if err != nil {
			return err
		}
		if deleted != "" {
			return errors.New(".gitattributes 的删除已暂存，请先处理该删除再暂存 LFS 文件")
		}
	}
	if _, err := InspectLFSExecutable(ctx, currentLFSExecutableOptions()); err != nil {
		return err
	}
	installed, _, problem, err := repo.lfsInstallation(ctx)
	if err != nil {
		return err
	}
	if !installed {
		return fmt.Errorf("请先初始化当前仓库的 Git LFS，再暂存 LFS 文件：%s", problem)
	}
	var rules strings.Builder
	for _, name := range names {
		rules.WriteString(lfsRule(name, true))
	}
	workAttributes := appendLFSRules(before.content, rules.String())
	stagedAttributes := appendLFSRules(indexAttributes.Text, rules.String())
	index, cleanup, err := repo.lfsTemporaryIndex(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	hash, err := repo.lfsGit(ctx, index, stagedAttributes, "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		return err
	}
	hash = strings.TrimSpace(hash)
	if err := validCommitHash(hash); err != nil {
		return err
	}
	mode := "100644"
	if entry := originalEntries[".gitattributes"]; strings.HasPrefix(entry, "100755 ") {
		mode = "100755"
	}
	if _, err := repo.lfsGit(ctx, index, mode+" "+hash+"\t.gitattributes\x00", "update-index", "--add", "-z", "--index-info"); err != nil {
		return err
	}
	currentFiles, err := repo.inspectWorktreeFiles(ctx, names)
	if err != nil {
		return err
	}
	if !sameInspectedFiles(currentFiles, originalFiles) {
		return errors.New("LFS 准备期间工作区文件已变化，请重新检查")
	}
	if err := repo.verifyLFSInspectionIndex(ctx, expected); err != nil {
		return err
	}
	appendedAttributes, err := appendLFSAttributes(root, before, workAttributes)
	if err != nil {
		return err
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, repo.rollbackLFSAttributes(root, before, appendedAttributes))
		}
	}()
	ruleFiles, err := repo.inspectWorktreeFiles(ctx, names)
	if err != nil {
		return err
	}
	// Fingerprint 只绑定内容样本和元数据；新规则允许改变 LFS/Binary 等属性字段。
	if !sameLFSContent(ruleFiles, originalFiles) {
		return errors.New("写入 LFS 规则期间工作区文件已变化，请重新检查")
	}
	// 根目录规则可能被子目录属性或 info/attributes 覆盖；禁止悄悄暂存成普通 Git blob。
	for _, cached := range []bool{false, true} {
		args := []string{"check-attr", "-z"}
		if cached {
			args = append(args, "--cached")
		}
		args = append(args, "filter", "--")
		output, err := repo.lfsGit(ctx, index, "", append(args, names...)...)
		if err != nil {
			return err
		}
		fields := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
		if len(fields) != 3*len(names) {
			return errors.New("无法验证所选文件的 LFS 属性")
		}
		for position := 0; position < len(fields); position += 3 {
			if fields[position+2] != "lfs" {
				return fmt.Errorf("文件 %s 的 LFS 属性被其他规则覆盖，请先手动整合属性", fields[position])
			}
		}
	}
	// 每个文件只经过一次 clean filter：已在索引中的文件即使 stat 未变也须 --renormalize 才会重新清洗，
	// 且 renormalize 本身从工作区读取内容，等价于先 add 再 renormalize；未跟踪文件不受 renormalize
	// 影响，普通 add 已按新的 lfs 属性清洗。临时索引复制自真实索引，originalEntries 即其跟踪状态，
	// 两者若在此期间分歧，后面的条目检查或发布前的索引比对会拒绝发布。
	var tracked, untracked []string
	for _, name := range names {
		if originalEntries[name] != "" {
			tracked = append(tracked, name)
		} else {
			untracked = append(untracked, name)
		}
	}
	// 列表为空时不能调用：不带路径的 add --renormalize 会重新清洗所有已跟踪文件。
	if len(untracked) > 0 {
		if _, err := repo.lfsGit(ctx, index, "", append([]string{"add", "--"}, untracked...)...); err != nil {
			return err
		}
	}
	if len(tracked) > 0 {
		if _, err := repo.lfsGit(ctx, index, "", append([]string{"add", "--renormalize", "--"}, tracked...)...); err != nil {
			return err
		}
	}
	updated, err := repo.lfsIndexEntries(ctx, index, allPaths)
	if err != nil {
		return err
	}
	entries, err := lfsEntriesByPath(updated)
	if err != nil {
		return err
	}
	objects := make([]string, 0, len(names))
	for _, name := range names {
		entry := entries[name]
		if entry == "" {
			return fmt.Errorf("没有生成 LFS 索引条目：%s", name)
		}
		objects = append(objects, strings.Fields(strings.SplitN(entry, "\t", 2)[0])[1])
	}
	if err := repo.verifyLFSPointers(ctx, index, names, objects); err != nil {
		return err
	}
	indexPath, indexLock, err := repo.lockLFSIndex(ctx)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		_ = indexLock.Close()
		if !published {
			_ = os.Remove(indexLock.Name())
		}
	}()
	current, err := repo.lfsIndexEntries(ctx, "", allPaths)
	if err != nil {
		return err
	}
	if current != original {
		return errors.New("所选文件的索引已被外部修改，请重试")
	}
	currentAttributes, err := readLFSAttributes(root)
	if err != nil {
		return err
	}
	if currentAttributes.content != workAttributes {
		return errors.New(".gitattributes 在转换期间已被外部修改，请检查规则后重试")
	}
	currentFiles, err = repo.inspectWorktreeFiles(ctx, names)
	if err != nil {
		return err
	}
	if !sameLFSContent(currentFiles, originalFiles) || !sameInspectedFiles(currentFiles, ruleFiles) {
		return errors.New("LFS 转换期间工作区文件或属性已变化，请重新检查；未写入真实索引")
	}
	if err := repo.verifyLFSInspectionIndex(ctx, expected); err != nil {
		return err
	}
	currentFlags, err := repo.git(ctx, append([]string{"ls-files", "-v", "-z", "--"}, allPaths...)...)
	if err != nil {
		return err
	}
	if currentFlags != flags {
		return errors.New("LFS 转换期间索引文件标记已变化，请重新检查；未写入真实索引")
	}
	// 锁内从最新索引合入指定条目，保留外部已完成的无关暂存；子进程只写临时索引。
	publication, publicationCleanup, err := repo.lfsTemporaryIndex(ctx)
	if err != nil {
		return err
	}
	defer publicationCleanup()
	if _, err := repo.lfsGit(ctx, publication, updated, "update-index", "--add", "-z", "--index-info"); err != nil {
		return err
	}
	source, err := os.Open(publication)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(indexLock, source)
	if err := errors.Join(copyErr, source.Close(), indexLock.Sync(), ctx.Err()); err != nil {
		return err
	}
	if err := indexLock.Close(); err != nil {
		return err
	}
	if err := os.Rename(indexLock.Name(), indexPath); err != nil {
		return err
	}
	published = true
	return nil
}

func (repo *Repository) lockLFSIndex(ctx context.Context) (string, *os.File, error) {
	output, err := repo.git(ctx, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", nil, err
	}
	path := strings.TrimSuffix(output, "\n")
	file, err := os.OpenFile(path+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if err != nil {
		return "", nil, err
	}
	if info, statErr := os.Stat(path); statErr == nil {
		err = file.Chmod(info.Mode().Perm())
	} else if !os.IsNotExist(statErr) {
		err = statErr
	}
	if err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", nil, err
	}
	return path, file, nil
}

// verifyLFSPointers 用两个批处理进程代替每文件两次 cat-file：先只读大小，
// 全部不超过 1024 字节才读取内容，避免失效 filter 导致大文件进入内存。
func (repo *Repository) verifyLFSPointers(ctx context.Context, index string, names, objects []string) error {
	input := strings.Join(objects, "\n") + "\n"
	output, err := repo.lfsGit(ctx, index, input, "cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != len(objects) {
		return errors.New("无法读取 LFS 指针对象")
	}
	for position, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != objects[position] || fields[1] != "blob" {
			return fmt.Errorf("文件 %s 未转换为 LFS 指针", names[position])
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 || size > 1024 {
			return fmt.Errorf("文件 %s 未转换为 LFS 指针", names[position])
		}
	}
	output, err = repo.lfsGit(ctx, index, input, "cat-file", "--batch")
	if err != nil {
		return fmt.Errorf("无法读取 LFS 指针：%w", errors.Join(err, errors.New("请检查 LFS filter 配置")))
	}
	for position, object := range objects {
		header, rest, ok := strings.Cut(output, "\n")
		fields := strings.Fields(header)
		size := int64(-1)
		if ok && len(fields) == 3 && fields[0] == object && fields[1] == "blob" {
			size, err = strconv.ParseInt(fields[2], 10, 64)
		}
		if size < 0 || err != nil || int64(len(rest)) < size+1 || rest[size] != '\n' {
			return errors.New("无法解析 LFS 指针对象")
		}
		if !validLFSPointer(rest[:size]) {
			return fmt.Errorf("文件 %s 未转换为 LFS 指针：请检查 LFS filter 配置", names[position])
		}
		output = rest[size+1:]
	}
	return nil
}
