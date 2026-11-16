// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type nativeMemoryDiagnostics struct {
	busy    bool
	path    string
	problem string
}

func (app *nativeApp) memoryDiagnosticsPreferences(c *ui.Context) {
	state := &app.memoryDiagnostics
	settingsSection(c, locales.Diagnostics(), func() {
		settingsRow(c, locales.MemoryDiagnostics(), func() {
			label := locales.ExportDiagnostics()
			if state.busy {
				label = locales.ExportingDiagnostics()
			}
			ui.Button(c, label).Label(locales.ExportMemoryDiagnostics()).AlignSelf(ui.Start).Disabled(state.busy || app.busy).
				OnClick(app.exportMemoryDiagnostics)
			ui.Text(c, locales.MemoryDiagnosticsHint()).FontSize(12).TextColor(c.Theme().TextMuted).FillWidth()
			if state.problem != "" {
				ui.Text(c, state.problem).FontSize(12).TextColor(c.Theme().Danger).FillWidth()
			}
			if state.path != "" && !state.busy {
				ui.Text(c, locales.DiagnosticsSaved(state.path)).FontSize(12).TextColor(c.Theme().TextMuted).
					FillWidth().Tooltip(state.path)
				label := locales.OpenDiagnosticsFolder()
				if runtime.GOOS == "darwin" {
					label = locales.RevealFinder()
				}
				ui.Button(c, label).AlignSelf(ui.Start).OnClick(func() {
					if err := mygo.Shell.OpenPath(state.path); err != nil {
						state.problem = locales.OpenDiagnosticsFailed(err.Error())
					}
				})
			}
		})
	})
}

func (app *nativeApp) exportMemoryDiagnostics() {
	state := &app.memoryDiagnostics
	if app.closed || app.busy || state.busy {
		return
	}
	state.busy = true
	state.problem = ""
	app.busy = true
	paths, err := app.openDialog(mygo.OpenDialogOptions{
		Parent: app.win, Title: locales.ExportMemoryDiagnostics(), ButtonLabel: locales.ExportDiagnosticsConfirm(),
		Directory: true, CreateDirectories: true, DefaultPath: app.directoryDialogPath(),
	})
	app.busy = false
	if err != nil || len(paths) == 0 || app.closed {
		state.busy = false
		if err != nil {
			state.problem = locales.ExportDiagnosticsFailed(err.Error())
		}
		return
	}
	// 导出状态属于应用，关闭设置后仍可完成；窗口退出时随 app.ctx 取消。
	ctx, post, parent := app.ctx, app.post, paths[0]
	go func() {
		path, err := writeMemoryDiagnostics(ctx, parent)
		post(func() {
			if app.closed {
				return
			}
			state.busy = false
			if err != nil {
				state.problem = locales.ExportDiagnosticsFailed(err.Error())
				return
			}
			state.path = path
		})
	}()
}

func writeMemoryDiagnostics(ctx context.Context, parent string) (directory string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	directory, err = os.MkdirTemp(parent, "51code-memory-"+time.Now().Format("20060102-150405")+"-")
	if err != nil {
		return "", err
	}
	// 仅清理本次创建的独立目录，不覆盖历史导出；失败时不留下看似完整的诊断文件。
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(directory))
			directory = ""
		}
	}()
	// heap 以最近一次 GC 为准，按需采集前回收一次，避免把已不可达对象误判为泄漏。
	runtime.GC()
	for _, name := range []string{"heap", "allocs", "goroutine"} {
		if err = ctx.Err(); err != nil {
			return directory, err
		}
		profile := pprof.Lookup(name)
		err = writeMemoryDiagnosticFile(filepath.Join(directory, name+".pprof"), func(writer io.Writer) error {
			return profile.WriteTo(writer, 0)
		})
		if err != nil {
			return directory, fmt.Errorf("%s.pprof: %w", name, err)
		}
	}
	if err = ctx.Err(); err != nil {
		return directory, err
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	err = writeMemoryDiagnosticFile(filepath.Join(directory, "memstats.json"), func(writer io.Writer) error {
		return json.NewEncoder(writer).Encode(struct {
			CapturedAt     time.Time        `json:"capturedAt"`
			GoVersion      string           `json:"goVersion"`
			OS             string           `json:"os"`
			Arch           string           `json:"arch"`
			MemProfileRate int              `json:"memProfileRate"`
			Memory         runtime.MemStats `json:"memory"`
		}{time.Now(), runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.MemProfileRate, memory})
	})
	if err == nil {
		err = ctx.Err()
	}
	return directory, err
}

func writeMemoryDiagnosticFile(path string, write func(io.Writer) error) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	return errors.Join(write(file), file.Close())
}
