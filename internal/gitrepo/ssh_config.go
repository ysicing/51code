// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func SSHHostAliases(ctx context.Context) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	configDir := filepath.Join(home, ".ssh")
	visited, seen := make(map[string]bool), make(map[string]bool)
	var hosts []string
	var read func(string, int)
	read = func(name string, depth int) {
		if ctx.Err() != nil || depth > 16 || len(visited) >= 128 || visited[name] {
			return
		}
		visited[name] = true
		info, err := os.Stat(name)
		if err != nil || !info.Mode().IsRegular() {
			return
		}
		file, err := os.Open(name)
		if err != nil {
			return
		}
		defer file.Close()
		lines := bufio.NewScanner(io.LimitReader(file, 1<<20))
		for ctx.Err() == nil && lines.Scan() {
			line := strings.TrimLeft(lines.Text(), " \t")
			end := strings.IndexAny(line, " \t=")
			if end < 0 {
				continue
			}
			args := strings.TrimLeft(strings.TrimPrefix(strings.TrimLeft(line[end:], " \t"), "="), " \t")
			switch strings.ToLower(line[:end]) {
			case "host":
				for _, host := range sshConfigWords(args) {
					key := strings.ToLower(host)
					if !seen[key] && !strings.Contains(host, "@") && ValidSSHHost(host) {
						seen[key] = true
						hosts = append(hosts, host)
					}
				}
			case "include":
				for _, pattern := range sshConfigWords(args) {
					if rest, ok := strings.CutPrefix(pattern, "~/"); ok {
						pattern = filepath.Join(home, rest)
					} else if !filepath.IsAbs(pattern) {
						pattern = filepath.Join(configDir, pattern)
					}
					matches, _ := filepath.Glob(pattern)
					for _, match := range matches {
						read(match, depth+1)
					}
				}
			}
		}
	}
	read(filepath.Join(configDir, "config"), 0)
	return hosts
}

func sshConfigWords(value string) []string {
	var words []string
	for value != "" {
		value = strings.TrimLeft(value, " \t\r")
		if value == "" || value[0] == '#' {
			break
		}
		var word strings.Builder
		var quote byte
		for len(value) > 0 {
			char := value[0]
			if quote == 0 && (char == ' ' || char == '\t' || char == '\r') {
				break
			}
			value = value[1:]
			switch {
			case char == quote:
				quote = 0
			case quote == 0 && (char == '"' || char == '\''):
				quote = char
			case char == '\\' && len(value) > 0 && (value[0] == '"' || value[0] == '\'' || value[0] == '\\'):
				word.WriteByte(value[0])
				value = value[1:]
			default:
				word.WriteByte(char)
			}
		}
		if quote != 0 {
			break
		}
		words = append(words, word.String())
	}
	return words
}
