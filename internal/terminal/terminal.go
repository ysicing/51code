// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package terminal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const System = "system"

// Validate 检查用户选择的应用；空值及 System 使用平台自带终端。
func Validate(application string) error {
	if application == "" || application == System {
		return nil
	}
	if !filepath.IsAbs(application) {
		return errors.New("terminal application path must be absolute")
	}
	info, err := os.Stat(application)
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		if !info.IsDir() || !strings.EqualFold(filepath.Ext(application), ".app") {
			return errors.New("select a terminal .app bundle")
		}
	case "windows":
		if info.IsDir() || !strings.EqualFold(filepath.Ext(application), ".exe") {
			return errors.New("select a terminal .exe application")
		}
	default:
		if info.IsDir() || info.Mode()&0111 == 0 {
			return errors.New("select an executable terminal application")
		}
	}
	return nil
}

// Start 启动独立终端并回收子进程；终端会话不随应用窗口关闭而取消。
func Start(directory, application string) (<-chan error, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", directory)
	}
	if err := Validate(application); err != nil {
		return nil, err
	}
	command, err := commandFor(runtime.GOOS, application, directory, exec.LookPath)
	if err != nil {
		return nil, err
	}
	return startProcess(command)
}

func commandFor(platform, application, directory string, lookup func(string) (string, error)) (*exec.Cmd, error) {
	if application == System {
		application = ""
	}
	var executable string
	var arguments []string
	switch platform {
	case "darwin":
		if application == "" {
			application = "Terminal"
		}
		executable, arguments = "/usr/bin/open", []string{"-a", application, "--", directory}
	case "windows":
		executable = application
		if executable == "" {
			var err error
			executable, err = lookup("cmd.exe")
			if err != nil {
				return nil, err
			}
		}
		if strings.EqualFold(filepath.Base(strings.ReplaceAll(executable, "\\", "/")), "wt.exe") {
			// Windows Terminal 会再次按分号拆分命令，独立参数中的路径也需要转义。
			arguments = []string{"-d", strings.ReplaceAll(directory, ";", `\;`)}
		}
	case "linux":
		executable = application
		if executable == "" {
			if path, err := lookup("xdg-terminal-exec"); err == nil {
				executable, arguments = path, []string{"--dir=" + directory}
			} else {
				path, err := lookup("x-terminal-emulator")
				if err != nil {
					return nil, errors.New("no system terminal found; select a terminal application in settings")
				}
				executable = path
			}
		}
		if arguments == nil {
			switch filepath.Base(executable) {
			case "gnome-terminal", "kgx", "tilix":
				arguments = []string{"--working-directory=" + directory}
			case "xfce4-terminal":
				arguments = []string{"--working-directory=" + directory}
			case "konsole":
				arguments = []string{"--workdir", directory}
			default:
				// 路径只作为位置参数传入固定脚本，避免空格、分号及命令替换被解释执行。
				arguments = []string{"-e", "/bin/sh", "-c", `cd -- "$1" && exec "${SHELL:-/bin/sh}"`, "51code-terminal", directory}
			}
		}
	default:
		return nil, fmt.Errorf("unsupported terminal platform: %s", platform)
	}
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	return command, nil
}
