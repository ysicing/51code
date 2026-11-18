// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ysicing/51code/internal/workspace"
)

func TestNativeScanKeepsPartialResults(t *testing.T) {
	found := []workspace.LocalRepository{{Name: "found", Path: "/repositories/found"}}
	result, err := keepPartialScan(workspace.ScanResult{Repositories: found, Warnings: []string{"跳过的目录"}}, context.DeadlineExceeded)
	if err != nil || !result.Truncated || len(result.Repositories) != 1 ||
		len(result.Warnings) != 2 || result.Warnings[0] != context.DeadlineExceeded.Error() {
		t.Fatalf("超时的部分结果被丢弃或缺少原因: %+v %v", result, err)
	}
	app, _, _ := nativeFixture(t)
	app.scanResult, app.scanSelected = &result, map[string]bool{}
	app.scanDialog()
	if app.dialog == nil || !strings.Contains(app.dialog.detail, context.DeadlineExceeded.Error()) {
		t.Fatal("部分结果对话框未提示扫描未完成")
	}
	for _, test := range []struct {
		name   string
		result workspace.ScanResult
		err    error
	}{
		{"用户取消", workspace.ScanResult{Repositories: found}, context.Canceled},
		{"没有结果", workspace.ScanResult{}, context.DeadlineExceeded},
		{"无效目录", workspace.ScanResult{}, errors.New("请选择文件夹")},
	} {
		if _, err := keepPartialScan(test.result, test.err); !errors.Is(err, test.err) {
			t.Fatalf("%s: 错误被吞掉: %v", test.name, err)
		}
	}
}
