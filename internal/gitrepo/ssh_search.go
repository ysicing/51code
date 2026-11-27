// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"bufio"
	"context"
	"errors"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

func (repo SSHWorkspace) WorktreeSearchPaths(ctx context.Context) ([]string, error) {
	var paths []string
	err := repo.records(ctx, func(name string) error {
		if validSSHBrowsePath(name) == nil {
			paths = append(paths, name)
		}
		return nil
	}, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return nil, err
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

func (repo SSHWorkspace) SearchWorktree(ctx context.Context, query string, publish func([]WorktreeSearchMatch)) (bool, error) {
	if query == "" || strings.ContainsAny(query, "\x00\r\n") {
		return false, nil
	}
	pattern, err := regexp.Compile(worktreeLiteralPattern(query))
	if err != nil {
		return false, err
	}
	process, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := repo.gitTo(process, "", writer, "-c", "grep.threads=1", "-c", "submodule.recurse=false", "-c", "grep.column=false",
			"grep", "--no-color", "--no-textconv", "--untracked",
			"--exclude-standard", "-I", "-i", "-F", "-n", "-z", "-e", query, "--")
		writer.CloseWithError(err)
		done <- err
	}()
	limited, parseErr := readSSHMatches(reader, pattern, publish)
	reader.Close()
	if limited || parseErr != nil {
		cancel()
	}
	err = <-done
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if limited || sshExitCode(err, 1) && (parseErr == nil || sshExitCode(parseErr, 1)) {
		return limited, nil
	}
	if parseErr != nil {
		return false, parseErr
	}
	return false, err
}

func readSSHMatches(input io.Reader, pattern *regexp.Regexp, publish func([]WorktreeSearchMatch)) (bool, error) {
	reader := bufio.NewReaderSize(input, 64<<10)
	var batch []WorktreeSearchMatch
	total := 0
	for {
		file, err := reader.ReadSlice(0)
		if err == io.EOF && len(file) == 0 {
			break
		}
		if err != nil {
			if sshExitCode(err, 1) && len(file) == 0 {
				break
			}
			return false, err
		}
		name := string(file[:len(file)-1])
		line, err := reader.ReadSlice(0)
		if err != nil {
			return false, err
		}
		number, err := strconv.Atoi(string(line[:len(line)-1]))
		if err != nil || number <= 0 || validSSHBrowsePath(name) != nil {
			return false, errors.New("远端工作区搜索格式无效")
		}
		var text []byte
		size := 0
		for {
			part, readErr := reader.ReadSlice('\n')
			size += len(part)
			if size <= fileContentLimit {
				text = append(text, part...)
			}
			if readErr != bufio.ErrBufferFull {
				if readErr != nil && readErr != io.EOF {
					return false, readErr
				}
				break
			}
		}
		if size <= fileContentLimit && utf8.Valid(text) {
			if match, ok := worktreeMatch(name, number, strings.TrimSuffix(string(text), "\n"), pattern); ok {
				if total == WorktreeSearchLimit {
					if len(batch) > 0 {
						publish(batch)
					}
					return true, nil
				}
				batch = append(batch, match)
				total++
				if len(batch) == 64 {
					publish(batch)
					batch = nil
				}
			}
		}
	}
	if len(batch) > 0 {
		publish(batch)
	}
	return false, nil
}
