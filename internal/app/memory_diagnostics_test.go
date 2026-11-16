// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestWriteMemoryDiagnostics(t *testing.T) {
	parent := t.TempDir()
	sentinel := filepath.Join(parent, "heap.pprof")
	if err := os.WriteFile(sentinel, []byte("保留已有文件"), 0600); err != nil {
		t.Fatal(err)
	}
	var previous string
	for range 2 {
		directory, err := writeMemoryDiagnostics(context.Background(), parent)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(directory) != parent || directory == previous {
			t.Fatalf("导出未创建独立目录: %s", directory)
		}
		previous = directory
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
			path := filepath.Join(directory, entry.Name())
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				t.Fatalf("诊断文件权限过宽: %s %v", path, info.Mode())
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if entry.Name() == "memstats.json" {
				var stats struct {
					GoVersion string
					Memory    runtime.MemStats
				}
				if err := json.Unmarshal(data, &stats); err != nil || stats.GoVersion != runtime.Version() || stats.Memory.HeapSys == 0 {
					t.Fatalf("运行时统计无效: %s %v", data, err)
				}
				continue
			}
			reader, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("%s 不是压缩的 pprof: %v", entry.Name(), err)
			}
			profile, err := io.ReadAll(reader)
			if closeErr := reader.Close(); err != nil || closeErr != nil || len(profile) == 0 {
				t.Fatalf("%s 内容无效: %v %v", entry.Name(), err, closeErr)
			}
		}
		if !slices.Equal(names, []string{"allocs.pprof", "goroutine.pprof", "heap.pprof", "memstats.json"}) {
			t.Fatalf("诊断文件不完整: %v", names)
		}
		info, err := os.Stat(directory)
		if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
			t.Fatalf("诊断目录权限无效: %v %v", info, err)
		}
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "保留已有文件" {
		t.Fatal("导出覆盖了已有文件", err)
	}
}

type cancelMemoryDiagnosticsContext struct {
	context.Context
	cancel context.CancelFunc
}

func (ctx cancelMemoryDiagnosticsContext) Err() error {
	err := ctx.Context.Err()
	ctx.cancel()
	return err
}

func TestWriteMemoryDiagnosticsFailureAndCancellation(t *testing.T) {
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name    string
		ctx     context.Context
		parent  string
		wantErr error
	}{
		{"missing-directory", context.Background(), filepath.Join(parent, "missing"), os.ErrNotExist},
		{"canceled-before-export", ctx, parent, context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, err := writeMemoryDiagnostics(test.ctx, test.parent)
			if path != "" || !errors.Is(err, test.wantErr) {
				t.Fatalf("错误或取消未正确返回: %q %v", path, err)
			}
		})
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	path, err := writeMemoryDiagnostics(cancelMemoryDiagnosticsContext{ctx, cancel}, parent)
	if path != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("导出中取消未正确返回: %q %v", path, err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("失败或取消后留下不完整目录: %v %v", entries, err)
	}
}

func TestWriteMemoryDiagnosticFileProtectsExistingFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heap.pprof")
	writeFailure := errors.New("写入失败")
	if err := writeMemoryDiagnosticFile(path, func(writer io.Writer) error {
		_, _ = io.WriteString(writer, "部分内容")
		return writeFailure
	}); !errors.Is(err, writeFailure) {
		t.Fatal("未返回写入错误", err)
	}
	if err := writeMemoryDiagnosticFile(path, func(io.Writer) error {
		t.Fatal("不应覆盖已有文件")
		return nil
	}); !errors.Is(err, os.ErrExist) {
		t.Fatal("未拒绝覆盖已有文件", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal("失败后文件仍被占用", err)
	}
}

func TestNativeMemoryDiagnosticsExport(t *testing.T) {
	t.Setenv("MYGO_LOCALE", "zh-Hans")
	app, _, finish := nativeFixture(t)
	tester := sidebarSettingsTester(t, app)
	parent := t.TempDir()
	calls := 0
	app.openDialog = func(options mygo.OpenDialogOptions) ([]string, error) {
		calls++
		if !app.busy || !app.memoryDiagnostics.busy || !options.Directory || options.Multiple ||
			!options.CreateDirectories || options.Title != "导出内存诊断" {
			t.Fatalf("保存目录选择参数无效: %+v", options)
		}
		app.exportMemoryDiagnostics()
		return []string{parent}, nil
	}
	clickNativeSettingsControl(t, tester, ui.RoleButton, "导出内存诊断")
	tester.Frame()
	button, found := tester.AccessByRole(ui.RoleButton, "导出内存诊断")
	if !found || !button.Disabled || !app.memoryDiagnostics.busy || app.busy {
		t.Fatalf("导出未防止重复点击或占用全局忙碌状态: %+v", button)
	}
	app.exportMemoryDiagnostics()
	if calls != 1 {
		t.Fatal("重复触发了目录选择")
	}
	if err := tester.Click("关闭设置"); err != nil {
		t.Fatal(err)
	}
	finish()
	if app.memoryDiagnostics.busy || app.memoryDiagnostics.problem != "" ||
		filepath.Dir(app.memoryDiagnostics.path) != parent {
		t.Fatalf("关闭设置后未完成导出: %+v", app.memoryDiagnostics)
	}
	app.settingsDialog()
	tester.Frame()
	reveal := locales.OpenDiagnosticsFolder()
	if runtime.GOOS == "darwin" {
		reveal = locales.RevealFinder()
	}
	nativeSettingsControl(t, tester, ui.RoleButton, reveal)
	if !tester.HasText(locales.DiagnosticsSaved(app.memoryDiagnostics.path)) {
		t.Fatal("重新打开设置未显示保存位置")
	}
}

func TestNativeMemoryDiagnosticsCancelFailureAndRetry(t *testing.T) {
	app, _, finish := nativeFixture(t)
	previous := filepath.Join(t.TempDir(), "previous")
	app.memoryDiagnostics.path = previous
	for _, failure := range []error{nil, errors.New("选择失败")} {
		app.openDialog = func(mygo.OpenDialogOptions) ([]string, error) { return nil, failure }
		app.exportMemoryDiagnostics()
		if app.busy || app.memoryDiagnostics.busy || app.memoryDiagnostics.path != previous ||
			(failure != nil) != (app.memoryDiagnostics.problem != "") {
			t.Fatalf("目录选择取消或失败丢失状态: %+v", app.memoryDiagnostics)
		}
	}
	parent := t.TempDir()
	app.openDialog = func(mygo.OpenDialogOptions) ([]string, error) {
		return []string{filepath.Join(parent, "missing")}, nil
	}
	app.exportMemoryDiagnostics()
	finish()
	if app.memoryDiagnostics.busy || app.memoryDiagnostics.problem == "" || app.memoryDiagnostics.path != previous {
		t.Fatalf("导出失败后未保留结果或恢复按钮: %+v", app.memoryDiagnostics)
	}
	app.openDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{parent}, nil }
	app.exportMemoryDiagnostics()
	finish()
	if app.memoryDiagnostics.busy || app.memoryDiagnostics.problem != "" || app.memoryDiagnostics.path == previous {
		t.Fatalf("导出失败后不能重试: %+v", app.memoryDiagnostics)
	}
	app.exportMemoryDiagnostics()
	app.closed = true
	previousState := app.memoryDiagnostics
	finish()
	if app.memoryDiagnostics != previousState {
		t.Fatal("关闭窗口后回填了迟到结果")
	}
}

func TestNativeMemoryDiagnosticsLayout(t *testing.T) {
	app, _, _ := nativeFixture(t)
	tester := sidebarSettingsTester(t, app)
	for _, language := range []string{"zh-Hans", "zh-Hant", "en"} {
		t.Run(language, func(t *testing.T) {
			t.Setenv("MYGO_LOCALE", language)
			tester.SetLocale(language)
			for _, size := range [][2]int{{1024, 680}, {780, 480}} {
				tester.SetSize(size[0], size[1])
				for _, dark := range []bool{false, true} {
					tester.SetDark(dark)
					for _, result := range []bool{false, true} {
						app.memoryDiagnostics.path = ""
						if result {
							app.memoryDiagnostics.path = filepath.Join(t.TempDir(), strings.Repeat("长路径-folder-", 12), "51code-memory-export")
						}
						tester.Frame()
						label := locales.ExportMemoryDiagnostics()
						if result {
							label = locales.OpenDiagnosticsFolder()
							if runtime.GOOS == "darwin" {
								label = locales.RevealFinder()
							}
						}
						for range 30 {
							scroll, found := tester.AccessByRole(ui.RoleScroll, locales.ApplicationSettings())
							if !found {
								t.Fatal("缺少设置滚动区域")
							}
							control, found := tester.AccessByRole(ui.RoleButton, label)
							if !found || control.Bounds.X < scroll.Bounds.X ||
								control.Bounds.X+control.Bounds.W > scroll.Bounds.X+scroll.Bounds.W {
								t.Fatalf("诊断按钮裁切或缺失: %+v", control)
							}
							if !control.Offscreen && control.Bounds.Y >= scroll.Bounds.Y &&
								control.Bounds.Y+control.Bounds.H <= scroll.Bounds.Y+scroll.Bounds.H {
								break
							}
							direction := float32(100)
							if control.Bounds.Y < scroll.Bounds.Y {
								direction = -100
							}
							tester.Scroll(scroll.Bounds.X+scroll.Bounds.W/2, scroll.Bounds.Y+scroll.Bounds.H/2, 0, direction)
						}
						control, _ := tester.AccessByRole(ui.RoleButton, label)
						if control.Offscreen {
							t.Fatalf("诊断按钮无法滚动到可见位置: %+v", control)
						}
						if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
							saveNativeScreenshot(t, tester, directory, fmt.Sprintf("settings-diagnostics-%s-%d-dark-%t-result-%t.png", language, size[1], dark, result))
						}
					}
				}
			}
		})
	}
}
