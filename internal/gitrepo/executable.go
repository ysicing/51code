// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// ExecutableOptions 指定应用使用的 Git，不改变系统环境变量。
type ExecutableOptions struct {
	Source    string `json:"source,omitempty"`
	Path      string `json:"path,omitempty"`
	Resources string `json:"-"`
}

// ExecutableInfo 是实际验证过的 Git 路径与版本。
type ExecutableInfo struct {
	Path    string
	Version string
}

var executableOptions atomic.Pointer[ExecutableOptions]

// ConfigureExecutable 原子替换后续命令的来源，已启动的进程继续使用原版本。
func ConfigureExecutable(options ExecutableOptions) {
	executableOptions.Store(&options)
}

func currentExecutableOptions() ExecutableOptions {
	if options := executableOptions.Load(); options != nil {
		return *options
	}
	return ExecutableOptions{Source: "system"}
}

// ResolveExecutable 只接受明确选择的来源，不在配置失效后静默切换版本。
func ResolveExecutable(options ExecutableOptions) (string, error) {
	return resolveExecutable(options, runtime.GOOS)
}

func resolveExecutable(options ExecutableOptions, platform string) (string, error) {
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
		path, err = exec.LookPath("git")
		if err != nil {
			return "", errors.New("未找到系统 Git，请在应用设置 → Git 中选择可用的 Git")
		}
	case "bundled":
		if platform != "windows" || options.Resources == "" {
			return "", errors.New("当前安装不包含内置 Git，请选择系统 Git 或自定义路径")
		}
		path = filepath.Join(options.Resources, "git", "cmd", "git.exe")
	case "custom":
		path = options.Path
		if !filepath.IsAbs(path) {
			return "", errors.New("请选择 Git 可执行文件的绝对路径")
		}
	default:
		return "", errors.New("无效的 Git 来源")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("未找到 Git 可执行文件，请在应用设置 → Git 中重新选择")
	}
	return path, nil
}

func executableEnvironment(path string) []string {
	environment := gitEnvironment()
	directories := executableDirectories(path, runtime.GOOS)
	for index, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "PATH") {
			environment[index] = key + "=" + strings.Join(directories, string(os.PathListSeparator)) + string(os.PathListSeparator) + value
			return environment
		}
	}
	return append(environment, "PATH="+strings.Join(directories, string(os.PathListSeparator)))
}

func executableDirectories(path, platform string) []string {
	// 子进程和 hooks 中的 git 必须与主命令一致；Windows 的 SSH、sh 和 DLL 随发行包提供。
	directories := []string{filepath.Dir(path)}
	if platform == "windows" {
		root := filepath.Dir(filepath.Dir(path))
		switch strings.ToLower(filepath.Base(root)) {
		case "ucrt64", "mingw64", "clangarm64", "mingw32", "usr":
			root = filepath.Dir(root)
		}
		for _, directory := range []string{"ucrt64", "mingw64", "clangarm64", "mingw32", "usr"} {
			candidate := filepath.Join(root, directory, "bin")
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				directories = append(directories, candidate)
			}
		}
	}
	return directories
}

func gitCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	path, err := ResolveExecutable(currentExecutableOptions())
	if err != nil {
		return nil, err
	}
	var command *exec.Cmd
	if ctx == nil {
		command = exec.Command(path, args...)
	} else {
		command = exec.CommandContext(ctx, path, args...)
	}
	command.Env = executableEnvironment(path)
	if options := lfsExecutableOptions.Load(); options != nil {
		// LFS 是可选工具；配置失效只由显式 LFS 操作报错，普通 Git 操作仍可运行。
		if lfsPath, err := ResolveLFSExecutable(*options); err == nil {
			if environment, err := lfsEnvironment(ctx, path, lfsPath); err == nil {
				command.Env = environment
			}
		}
	}
	return command, nil
}

// InspectExecutable 在后台调用，验证所选程序确实可以运行 Git。
func InspectExecutable(ctx context.Context, options ExecutableOptions) (ExecutableInfo, error) {
	path, err := ResolveExecutable(options)
	if err != nil {
		return ExecutableInfo{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, "--version")
	command.Env = executableEnvironment(path)
	command.WaitDelay = 2 * time.Second
	var output limitedBuffer
	command.Stdout, command.Stderr = &output, &output
	if err := runProcess(ctx, command); err != nil {
		return ExecutableInfo{}, fmt.Errorf("无法运行所选 Git：%w", err)
	}
	version := strings.TrimSpace(output.String())
	if output.overflow || !strings.HasPrefix(version, "git version ") {
		return ExecutableInfo{}, errors.New("所选程序不是有效的 Git")
	}
	return ExecutableInfo{Path: path, Version: version}, nil
}
