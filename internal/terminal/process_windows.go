// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package terminal

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

func startProcess(command *exec.Cmd) (<-chan error, error) {
	executable, err := windows.UTF16PtrFromString(command.Path)
	if err != nil {
		return nil, err
	}
	arguments, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(command.Args[1:]))
	if err != nil {
		return nil, err
	}
	directory, err := windows.UTF16PtrFromString(command.Dir)
	if err != nil {
		return nil, err
	}
	// ShellExecute 创建交互式控制台，避免继承 GUI 进程的空标准输入而立即退出。
	if err := windows.ShellExecute(0, nil, executable, arguments, directory, windows.SW_SHOWNORMAL); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	done <- nil
	close(done)
	return done, nil
}
