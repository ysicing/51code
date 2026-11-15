//go:build !windows

// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package terminal

import "os/exec"

func startProcess(command *exec.Cmd) (<-chan error, error) {
	if err := command.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
		close(done)
	}()
	return done, nil
}
