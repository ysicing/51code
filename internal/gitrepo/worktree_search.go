// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	WorktreeSearchLimit = 1000
	worktreeSearchBatch = 4 << 20
)

type WorktreeSearchMatch struct {
	Path   string
	Line   int
	Column int
	Text   string
}

func (repo *Repository) WorktreeSearchPaths(ctx context.Context) ([]string, error) {
	output, err := repo.git(ctx, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--")
	if err != nil {
		return nil, err
	}
	var paths []string
	for path := range strings.SplitSeq(output, "\x00") {
		if path != "" && validBrowsePath(path) == nil {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

func (repo *Repository) SearchWorktree(ctx context.Context, query string, publish func([]WorktreeSearchMatch)) (bool, error) {
	return repo.searchWorktree(ctx, query, findRipgrep(), publish)
}

func findRipgrep() string {
	candidates := []string{"rg"}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/opt/homebrew/bin/rg", "/usr/local/bin/rg")
	}
	for _, candidate := range candidates {
		if executable, err := exec.LookPath(candidate); err == nil {
			return executable
		}
	}
	return ""
}

type worktreeSearchFile struct {
	path string
	text string
}

func (repo *Repository) searchWorktree(ctx context.Context, query, rg string, publish func([]WorktreeSearchMatch)) (bool, error) {
	if query == "" || strings.ContainsAny(query, "\r\n\x00") {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	paths, err := repo.WorktreeSearchPaths(ctx)
	if err != nil {
		return false, err
	}
	root, err := os.OpenRoot(repo.Path)
	if err != nil {
		return false, err
	}
	defer root.Close()
	pattern, err := regexp.Compile(worktreeLiteralPattern(query))
	if err != nil {
		return false, err
	}
	var files []worktreeSearchFile
	remaining, size := WorktreeSearchLimit+1, 0
	flush := func() {
		matches, ok := grepWorktreeBatch(ctx, rg, pattern, files, remaining)
		if ctx.Err() != nil {
			return
		}
		if !ok {
			rg = ""
			matches = scanWorktreeBatch(ctx, pattern, files, remaining)
		}
		clear(files)
		files, size = files[:0], 0
		remaining -= len(matches)
		if remaining == 0 {
			matches = matches[:len(matches)-1]
		}
		if len(matches) != 0 && ctx.Err() == nil {
			publish(matches)
		}
	}
	lastFlush := time.Now()
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		text := readWorktreeSearchFile(root, path)
		if text == "" {
			continue
		}
		files = append(files, worktreeSearchFile{path: path, text: text})
		size += len(text)
		if size >= worktreeSearchBatch || time.Since(lastFlush) >= 180*time.Millisecond {
			flush()
			lastFlush = time.Now()
			if remaining == 0 {
				return true, ctx.Err()
			}
		}
	}
	if len(files) > 0 && ctx.Err() == nil {
		flush()
	}
	return remaining == 0, ctx.Err()
}

func worktreeLiteralPattern(query string) string {
	var pattern strings.Builder
	for _, letter := range query {
		next := unicode.SimpleFold(letter)
		if next == letter {
			pattern.WriteString(regexp.QuoteMeta(string(letter)))
			continue
		}
		// 两个后端使用同一组显式折叠字符，避免 Unicode 表版本不同导致漏报。
		pattern.WriteString(`[\x{`)
		pattern.WriteString(strconv.FormatInt(int64(letter), 16))
		for ; next != letter; next = unicode.SimpleFold(next) {
			pattern.WriteString(`}\x{`)
			pattern.WriteString(strconv.FormatInt(int64(next), 16))
		}
		pattern.WriteString(`}]`)
	}
	return pattern.String()
}

func readWorktreeSearchFile(root *os.Root, path string) string {
	file, err := openFileContentPath(root, path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > fileContentLimit {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(file, fileContentLimit+1))
	if err != nil || len(data) > fileContentLimit || bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return ""
	}
	return string(data)
}

func scanWorktreeBatch(ctx context.Context, pattern *regexp.Regexp, files []worktreeSearchFile, limit int) []WorktreeSearchMatch {
	var matches []WorktreeSearchMatch
	for _, file := range files {
		lineNumber := 0
		for line := range strings.SplitSeq(file.text, "\n") {
			if ctx.Err() != nil {
				return matches
			}
			lineNumber++
			if match, ok := worktreeMatch(file.path, lineNumber, line, pattern); ok {
				matches = append(matches, match)
				if len(matches) == limit {
					return matches
				}
			}
		}
	}
	return matches
}

// rg 只读由 51Code 打开的文件内容，避免子进程重新解析路径时穿过替换后的符号链接。
func grepWorktreeBatch(ctx context.Context, executable string, pattern *regexp.Regexp, files []worktreeSearchFile, limit int) ([]WorktreeSearchMatch, bool) {
	if executable == "" {
		return nil, false
	}
	var input strings.Builder
	startLines := make([]int, 0, len(files))
	nextLine := 1
	for _, file := range files {
		startLines = append(startLines, nextLine)
		input.WriteString(file.text)
		nextLine += strings.Count(file.text, "\n")
		if !strings.HasSuffix(file.text, "\n") {
			input.WriteByte('\n')
			nextLine++
		}
	}
	command := exec.CommandContext(ctx, executable, "--no-config", "--encoding", "none", "--case-sensitive",
		"--text", "--line-number", "--no-filename", "--color", "never",
		"--max-count", strconv.Itoa(limit), "-e", pattern.String(), "-")
	command.SysProcAttr = detachedProcessAttributes()
	command.Stdin = strings.NewReader(input.String())
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		return nil, errors.As(err, &exit) && exit.ExitCode() == 1 && ctx.Err() == nil
	}
	var matches []WorktreeSearchMatch
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(nil, fileContentLimit+64)
	fileIndex, previousLine := 0, 0
	for scanner.Scan() {
		number, line, ok := strings.Cut(scanner.Text(), ":")
		lineNumber, parseErr := strconv.Atoi(number)
		if !ok || parseErr != nil || lineNumber <= previousLine || lineNumber >= nextLine {
			return nil, false
		}
		previousLine = lineNumber
		for fileIndex+1 < len(files) && startLines[fileIndex+1] <= lineNumber {
			fileIndex++
		}
		match, ok := worktreeMatch(files[fileIndex].path, lineNumber-startLines[fileIndex]+1, line, pattern)
		if !ok || len(matches) >= limit {
			return nil, false
		}
		matches = append(matches, match)
	}
	return matches, scanner.Err() == nil
}

func worktreeMatch(path string, lineNumber int, line string, pattern *regexp.Regexp) (WorktreeSearchMatch, bool) {
	line = strings.TrimSuffix(line, "\r")
	match := pattern.FindStringIndex(line)
	if match == nil {
		return WorktreeSearchMatch{}, false
	}
	start := max(0, match[0]-96)
	for !utf8.RuneStart(line[start]) {
		start++
	}
	end := min(len(line), match[0]+384)
	for end < len(line) && !utf8.RuneStart(line[end]) {
		end--
	}
	return WorktreeSearchMatch{Path: path, Line: lineNumber, Column: match[0], Text: strings.Clone(line[start:end])}, true
}
