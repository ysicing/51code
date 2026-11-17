// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"path/filepath"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"

	"github.com/egoist/mygo/ui"
)

func TestNativeFilePathMenusCopyClickedFile(t *testing.T) {
	app, root, _ := nativeFixture(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	app.writable = false
	path := "目录 with spaces/deleted.go"
	for _, tree := range []bool{false, true} {
		app.prefs.FileTree = tree
		for _, revision := range []bool{false, true} {
			selected := 0
			chosen := ""
			files := []gitrepo.CommitFile{{Path: "selected.go", Status: "M"}, {Path: path, Status: "D"}}
			app.selectedFile = "selected.go"
			var clipboard ui.Services
			list := &ui.ListState{}
			tester := ui.NewTester(func(c *ui.Context) {
				clipboard = c.Services()
				if revision {
					app.revisionFileList(c, files, list, &selected, "文件", "", func(path string) { chosen = path })
				} else {
					ui.Column(c).Fill().Children(func() {
						if tree {
							parts := ui.TreeBase(c)
							parts.Root.Fill().Children(func() {
								app.fileTree(c, parts, nativeFileNodes([]gitrepo.File{{Path: path, Index: "D"}}, "path", false, true), true, 0)
							})
						} else {
							app.changeFileRow(c, gitrepo.File{Path: path, Index: "D"}, true, nil, 0)
						}
					})
				}
			}, 640, 480)
			for _, item := range []struct{ label, want string }{
				{"复制相对路径", path},
				{"复制完整路径", filepath.Join(root, filepath.FromSlash(path))},
			} {
				if err := tester.RightClick(path); err != nil {
					t.Fatalf("tree=%t revision=%t: %v", tree, revision, err)
				}
				if err := tester.ChooseMenuItem(item.label); err != nil {
					t.Fatal(err)
				}
				if got := clipboard.ReadClipboard(); got != item.want {
					t.Fatalf("clipboard=%q, want %q", got, item.want)
				}
				if chosen != "" || app.selectedFile != "selected.go" || files[selected].Path != "selected.go" {
					t.Fatal("复制路径改变了当前选择或加载了其他文件")
				}
			}
		}
	}
}

func TestNativeDiffHeaderCopiesFilePath(t *testing.T) {
	app, root, _ := nativeFixture(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	app.writable = false
	app.selectedFile = "目录 with spaces/deleted.go"
	var clipboard ui.Services
	tester := ui.NewTester(func(c *ui.Context) {
		clipboard = c.Services()
		app.fileDiffPanel(c)
	}, 640, 480)
	if err := tester.Click("复制路径"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("复制完整路径"); err != nil {
		t.Fatal(err)
	}
	if got, want := clipboard.ReadClipboard(), filepath.Join(root, filepath.FromSlash(app.selectedFile)); got != want {
		t.Fatalf("clipboard=%q, want %q", got, want)
	}
}
