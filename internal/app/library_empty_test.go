// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/internal/workspace"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func nativeLibrarySearchVisible(tester *ui.Tester) bool {
	return slices.ContainsFunc(tester.AccessAll(), func(node ui.AccessInfo) bool {
		return node.Placeholder == locales.SearchRepositories() && !node.Offscreen
	})
}

func TestNativeEmptyLibraryLayout(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.library = workspace.Library{}
	app.showLibrary()
	for _, size := range [][2]int{{1024, 680}, {1320, 860}, {1551, 1014}} {
		for _, appearance := range []string{appearanceLight, appearanceDark} {
			app.prefs.Appearance = appearance
			tester := ui.NewTester(app.view, size[0], size[1])
			if nativeLibrarySearchVisible(tester) || tester.HasText(locales.EmptyRepositories()) {
				t.Fatal("空仓库仍显示搜索框或无操作提示")
			}
			for _, label := range []string{locales.Add(), locales.Clone(), locales.EnterPath(), locales.ScanFolders()} {
				count := 0
				for _, node := range tester.AccessAll() {
					if node.Role == ui.RoleButton && node.Name == label && !node.Offscreen {
						count++
						if node.Disabled || node.Bounds.Y < float32(size[1])/3 ||
							node.Bounds.Y+node.Bounds.H > float32(size[1])*2/3 {
							t.Fatalf("空仓库操作未居中或被禁用: %s %+v", label, node)
						}
					}
				}
				if count != 1 {
					t.Fatalf("操作应只有一个可见入口: %s count=%d", label, count)
				}
			}
			primary, _ := tester.AccessByRole(ui.RoleButton, locales.Add())
			clone, _ := tester.AccessByRole(ui.RoleButton, locales.Clone())
			path, _ := tester.AccessByRole(ui.RoleButton, locales.EnterPath())
			scan, _ := tester.AccessByRole(ui.RoleButton, locales.ScanFolders())
			center := app.prefs.Sidebar + (float32(size[0])-app.prefs.Sidebar)/2
			if delta := (primary.Bounds.X+clone.Bounds.X+clone.Bounds.W)/2 - center; delta < -2 || delta > 2 {
				t.Fatalf("主按钮未在内容区水平居中: %+v center=%v", primary, center)
			}
			if primary.Bounds.Y != clone.Bounds.Y || primary.Bounds.Y+primary.Bounds.H >= path.Bounds.Y || path.Bounds.Y != scan.Bounds.Y {
				t.Fatal("次级入口未排列在主按钮下方")
			}
			for _, label := range []string{locales.Fetch(), locales.Pull(), locales.Push()} {
				if _, found := tester.AccessByRole(ui.RoleButton, label); found {
					t.Fatal("空仓库列表显示了上次仓库的 Git 操作", label)
				}
			}
			if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
				saveNativeScreenshot(t, tester, directory, fmt.Sprintf("native-empty-library-%d-%s.png", size[0], appearance))
			}
		}
	}
	app.busy = true
	tester := ui.NewTester(app.view, 1024, 680)
	for _, label := range []string{locales.Add(), locales.Clone(), locales.EnterPath(), locales.ScanFolders()} {
		button, found := tester.AccessByRole(ui.RoleButton, label)
		if !found || !button.Disabled {
			t.Fatal("忙碌时未禁用中央操作", label)
		}
	}
}

func TestNativeEmptyLibraryAddsIntoCurrentGroup(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprint(grouped), func(t *testing.T) {
			app, path, finish := nativeFixture(t)
			var err error
			app.library, err = app.service.RemoveRepository(app.repository.snapshot.Path)
			if err != nil {
				t.Fatal(err)
			}
			if grouped {
				app.library, err = app.service.CreateGroup("日常开发")
				if err != nil {
					t.Fatal(err)
				}
				app.group = app.library.Groups[0].ID
			}
			app.showLibrary()
			tester := ui.NewTester(app.view, 1024, 680)
			if err := tester.Click(locales.EnterPath()); err != nil {
				t.Fatal(err)
			}
			tester.Key(0, ui.KeyEscape)
			if app.dialog != nil || !app.libraryEmpty() {
				t.Fatal("取消添加后未保留空状态")
			}
			if err := tester.Click(locales.EnterPath()); err != nil {
				t.Fatal(err)
			}
			if err := tester.Click(locales.FullRepositoryPath()); err != nil {
				t.Fatal(err)
			}
			tester.Type(path)
			if err := tester.Click(locales.Add()); err != nil {
				t.Fatal(err)
			}
			finish()
			tester.Frame()
			if app.problem != "" || app.dialog != nil || app.screen != "library" || app.libraryEmpty() ||
				len(app.library.Repositories) != 1 || app.library.Repositories[0].GroupID != app.group {
				t.Fatalf("添加后未恢复当前分组的列表: group=%s library=%+v problem=%s", app.group, app.library, app.problem)
			}
			if !nativeLibrarySearchVisible(tester) {
				t.Fatal("添加后未恢复搜索框")
			}
			for _, label := range []string{locales.Add(), locales.EnterPath(), locales.ScanFolders()} {
				button, found := tester.AccessByRole(ui.RoleButton, label)
				if !found || button.Bounds.Y+button.Bounds.H > 52 {
					t.Fatal("添加后未恢复顶部操作", label)
				}
			}
		})
	}
}

func TestNativeAddDirectoryOffersGitInit(t *testing.T) {
	for _, enterPath := range []bool{false, true} {
		t.Run(fmt.Sprint(enterPath), func(t *testing.T) {
			app, _, finish := nativeFixture(t)
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			writeNativeFile(t, directory, "notes.txt", "keep me\n")
			writeNativeFile(t, directory, "ignored.tmp", "ignore me\n")
			if enterPath {
				app.prefs.GitignoreTemplate = "*.tmp\n"
			}
			app.library, err = app.service.CreateGroup("日常开发")
			if err != nil {
				t.Fatal(err)
			}
			app.group = app.library.Groups[0].ID
			app.showLibrary()
			app.openDialog = func(options mygo.OpenDialogOptions) ([]string, error) {
				if !options.Directory || !options.Multiple {
					t.Fatalf("unexpected picker options: %+v", options)
				}
				return []string{directory}, nil
			}
			tester := ui.NewTester(app.view, 1024, 680)
			click := func(label string) {
				t.Helper()
				if err := tester.Click(label); err != nil {
					t.Fatal(err)
				}
			}
			if enterPath {
				click(locales.EnterPath())
				click(locales.FullRepositoryPath())
				tester.Type(directory)
				click(locales.Add())
			} else {
				click(locales.Add())
			}
			finish()
			tester.Frame()
			if app.dialog == nil || app.dialog.title != locales.InitRepositoryTitle() || app.problem != "" {
				t.Fatalf("expected initialization confirmation, got %v, %s", app.dialog, app.problem)
			}
			tester.Key(0, ui.KeyEscape)
			if _, err := os.Lstat(filepath.Join(directory, ".git")); !os.IsNotExist(err) {
				t.Fatalf("cancelled initialization created Git metadata: %v", err)
			}
			if enterPath {
				if app.dialog == nil || app.dialog.fields[0].value != directory {
					t.Fatal("cancelling should retain the entered path")
				}
				click(locales.Add())
			} else {
				click(locales.Add())
			}
			finish()
			tester.Frame()
			if output := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); output != "" && !enterPath {
				app.prefs.Appearance = appearanceLight
				tester.Frame()
				saveNativeScreenshot(t, tester, output, "add-repository-initialize-light.png")
				app.prefs.Appearance = appearanceDark
				tester.Frame()
				saveNativeScreenshot(t, tester, output, "add-repository-initialize-dark.png")
			}
			click(locales.InitRepository())
			finish()
			finish()
			if app.problem != "" || app.dialog != nil || app.screen != "changes" ||
				app.repository.snapshot.Path != directory || app.repository.snapshot.Branch != "main" ||
				!app.repository.snapshot.Unborn {
				t.Fatalf("initialized repository did not open: %s %s", app.screen, app.problem)
			}
			if !slices.ContainsFunc(app.library.Repositories, func(entry workspace.LocalRepository) bool {
				return entry.Path == directory && entry.GroupID == app.group
			}) {
				t.Fatal("initialized repository did not join the current group")
			}
			if !slices.Contains(app.repository.snapshot.Files, gitrepo.File{Path: "notes.txt", Index: "?", Working: "?"}) {
				t.Fatalf("existing file did not remain untracked: %+v", app.repository.snapshot.Files)
			}
			content, err := os.ReadFile(filepath.Join(directory, ".gitignore"))
			if err != nil || string(content) != app.prefs.GitignoreTemplate {
				t.Fatalf("unexpected initialized template %q: %v", content, err)
			}
			if enterPath && nativeGit(t, directory, "check-ignore", "ignored.tmp") != "ignored.tmp" {
				t.Fatal("Git did not apply the customized ignore rule")
			}
		})
	}
}

func TestNativeLibrarySearchAndEmptyGroup(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.showLibrary()
	app.query = "no-such-repository"
	tester := ui.NewTester(app.view, 1024, 680)
	if !tester.HasText(locales.NoMatchingRepositories()) || app.libraryEmpty() {
		t.Fatal("搜索无结果被误认为首次添加")
	}
	if !nativeLibrarySearchVisible(tester) {
		t.Fatal("搜索无结果隐藏了搜索框")
	}
	if err := tester.Click(locales.ClearSearch()); err != nil {
		t.Fatal(err)
	}
	if app.query != "" || app.libraryEmpty() {
		t.Fatal("清空搜索未恢复已有列表")
	}
	app.group = "empty-group"
	app.library.Groups = append(app.library.Groups, workspace.Group{ID: app.group, Name: "空分组"})
	tester.Frame()
	button, found := tester.AccessByRole(ui.RoleButton, locales.Add())
	if !found || button.Bounds.Y < 200 || !app.libraryEmpty() || !tester.HasText("空分组") {
		t.Fatal("其他分组已有仓库时未显示当前空分组的添加入口")
	}
	app.scanResult = &workspace.ScanResult{}
	app.scanDialog()
	if app.dialog.choice != "空分组" {
		t.Fatal("扫描添加未默认选中当前分组")
	}
}
