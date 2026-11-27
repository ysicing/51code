// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSSHHostStatsLinuxCounters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("采样脚本在远端 POSIX shell 中运行")
	}
	root := t.TempDir()
	write(t, root, "loadavg", "0.42 0.40 0.38 1/200 123\n")
	write(t, root, "net/route", "Iface Destination Gateway Flags RefCnt Use Metric Mask\n"+
		"wlan0 00000000 01020304 0003 0 0 600 00000000\n"+
		"eth0 00000000 0100000a 0003 0 0 100 00000000\n")
	write(t, root, "net/dev", "wlan0: 24 0 0 0 0 0 0 0 42 0 0 0 0 0 0 0\n"+
		"  eth0:12345678901234567 0 0 0 0 0 0 0 12345678901234568 0 0 0 0 0 0 0\n")
	script := strings.ReplaceAll(sshHostStatsScript, "/proc/", sshShellWord(filepath.ToSlash(root))+"/")
	raw, err := exec.CommandContext(t.Context(), "sh", "-c", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	stats := parseSSHHostStats(string(raw))
	if !stats.HasLoad || stats.Load != [3]float64{0.42, 0.40, 0.38} || stats.Interface != "eth0" ||
		stats.Received != 12345678901234567 || stats.Transmitted != 12345678901234568 {
		t.Fatalf("远端采样错误: %+v", stats)
	}
	partial := parseSSHHostStats("load NaN 0 0\ncpus 4\nnet eth0 -1 0\n")
	if partial.HasLoad || partial.CPUs != 4 || partial.Interface != "" {
		t.Fatalf("无效或缺失指标应独立降级: %+v", partial)
	}
}
