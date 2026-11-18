// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/gops/signal"
)

func TestGopsAgentDiagnosticsAndCleanup(t *testing.T) {
	t.Setenv("XSOURCETREE_GOPS", "1")
	directory := t.TempDir()
	t.Setenv("GOPS_CONFIG_DIR", directory)
	stop := startGopsAgent()
	t.Cleanup(stop)
	address := gopsTestAddress(t, directory)
	for _, test := range []struct {
		name    string
		command byte
		want    string
	}{
		{"version", signal.Version, runtime.Version()},
		{"stack", signal.StackTrace, "goroutine"},
		{"memstats", signal.MemStats, "alloc:"},
		{"stats", signal.Stats, "goroutines:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := gopsTestRequest(t, address, test.command)
			if !strings.Contains(result, test.want) {
				t.Fatalf("诊断响应缺少 %q: %s", test.want, result)
			}
		})
	}
	stop()
	stop()
	if _, err := os.Stat(filepath.Join(directory, strconv.Itoa(os.Getpid()))); !os.IsNotExist(err) {
		t.Fatalf("退出后 PID 文件未清理: %v", err)
	}
	if connection, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		connection.Close()
		t.Fatal("退出后仍接受诊断连接")
	}
}

func TestGopsAgentDisabledUnlessExplicitlyEnabled(t *testing.T) {
	for _, value := range []string{"", "0", "true"} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			t.Setenv("XSOURCETREE_GOPS", value)
			directory := t.TempDir()
			t.Setenv("GOPS_CONFIG_DIR", directory)
			stop := startGopsAgent()
			t.Cleanup(stop)
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("未显式开启诊断时仍写入发现文件: %v %v", entries, err)
			}
		})
	}
}

func TestGopsAgentStartFailureReleasesListener(t *testing.T) {
	t.Setenv("XSOURCETREE_GOPS", "1")
	directory := t.TempDir()
	blocked := filepath.Join(directory, "blocked")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOPS_CONFIG_DIR", blocked)
	stop := startGopsAgent()
	t.Cleanup(stop)
	t.Setenv("GOPS_CONFIG_DIR", directory)
	retryStop := startGopsAgent()
	t.Cleanup(retryStop)
	result := gopsTestRequest(t, gopsTestAddress(t, directory), signal.Version)
	if strings.TrimSpace(result) != runtime.Version() {
		t.Fatalf("初始化失败后无法重新启动诊断: %q", result)
	}
}

func gopsTestAddress(t *testing.T, directory string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(directory, strconv.Itoa(os.Getpid())))
	if err != nil {
		t.Fatalf("读取 gops 发现文件: %v", err)
	}
	port, err := strconv.Atoi(string(content))
	if err != nil || port <= 0 || port > 65535 {
		t.Fatalf("gops 端口无效: %q %v", content, err)
	}
	return net.JoinHostPort("127.0.0.1", string(content))
}

func gopsTestRequest(t *testing.T, address string, command byte) string {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte{command}); err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(io.LimitReader(connection, 16<<20))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
