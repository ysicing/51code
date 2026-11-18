// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func TestNativeTruncatedWorkingDiffRemainsVisible(t *testing.T) {
	app, path, finish := nativeFixture(t)
	writeNativeFile(t, path, "hello.txt", strings.Repeat("before text line\n", 150000))
	nativeGit(t, path, "commit", "-am", "base")
	writeNativeFile(t, path, "hello.txt", strings.Repeat("after text line\n", 150000))
	app.refresh()
	finish()
	app.selectFile("hello.txt", false)
	finish()
	if app.problem != "" || !app.diff.Truncated || app.rawDiff.Buffer == nil || app.patch.Supported ||
		!strings.Contains(app.rawDiff.Buffer.String(), "-before text line") {
		t.Fatalf("截断预览不可用：problem=%q truncated=%t patch=%t", app.problem, app.diff.Truncated, app.patch.Supported)
	}
}

func TestNativeChangesTreeKeepsConflictSelectionOnCancel(t *testing.T) {
	app, path, finish := nativeFixture(t)
	nativeGit(t, path, "checkout", "-b", "incoming")
	writeNativeFile(t, path, "hello.txt", "传入\n")
	nativeGit(t, path, "commit", "-am", "传入变更")
	nativeGit(t, path, "checkout", "main")
	writeNativeFile(t, path, "hello.txt", "当前\n")
	nativeGit(t, path, "commit", "-am", "当前变更")
	if err := exec.Command("git", "-C", path, "merge", "incoming").Run(); err == nil {
		t.Fatal("预期合并冲突")
	}
	writeNativeFile(t, path, "other.txt", "其他文件\n")
	app.refresh()
	finish()
	app.prefs.FileTree = true
	app.selectFile("hello.txt", false)
	finish()
	if app.conflict == nil {
		t.Fatal("未打开冲突文件")
	}
	app.conflictEdit.Buffer = editor.NewBuffer("冲突草稿\n")
	outline := newNativeFileOutline()
	tree := app.fileSection(false).sortedFiles("path", false).fileTree(false)
	tester := ui.NewTester(func(c *ui.Context) {
		app.changesFileTree(c, outline, tree, false, "未暂存", 360)
		if app.dialog != nil {
			app.dialogView(c)
		}
	}, 640, 480)
	if err := tester.Click("other.txt"); err != nil {
		t.Fatal(err)
	}
	if app.dialog == nil {
		t.Fatal("未确认是否放弃冲突草稿")
	}
	if err := tester.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if node := outline.chosen(); node == nil || node.path != "hello.txt" ||
		app.selectedFile != "hello.txt" || app.conflictEdit.Buffer.String() != "冲突草稿\n" {
		t.Fatalf("取消切换后光标或草稿未保留：%+v，选中=%q", node, app.selectedFile)
	}
}

func TestNativeFileSectionCacheTracksFilterSortAndSnapshot(t *testing.T) {
	app := &nativeApp{prefs: &nativePreferences{}, repository: &repositoryView{}}
	app.repository.snapshot.Files = []gitrepo.File{
		{Path: "b.go", Index: " ", Working: "M"}, {Path: "a.go", Index: " ", Working: "M"}, {Path: "c.txt", Index: "A", Working: " "},
	}
	paths := func(files []gitrepo.File) []string {
		var result []string
		for _, file := range files {
			result = append(result, file.Path)
		}
		return result
	}
	if got := paths(app.visibleFiles(false)); !slices.Equal(got, []string{"b.go", "a.go"}) {
		t.Fatalf("未暂存分组=%v", got)
	}
	section := app.fileSection(false)
	if sorted := section.sortedFiles("path", false); !slices.Equal(paths(sorted.files), []string{"a.go", "b.go"}) {
		t.Fatalf("排序结果=%v", paths(sorted.files))
	}
	if sorted := section.sortedFiles("path", true); !slices.Equal(paths(sorted.files), []string{"b.go", "a.go"}) {
		t.Fatalf("排序方向变化后未重排: %v", paths(sorted.files))
	}
	if got := paths(app.visibleFiles(false)); !slices.Equal(got, []string{"b.go", "a.go"}) {
		t.Fatalf("排序改写了快照顺序: %v", got)
	}
	app.fileQuery = "A."
	if got := paths(app.visibleFiles(false)); !slices.Equal(got, []string{"a.go"}) {
		t.Fatalf("过滤词变化后未重新过滤: %v", got)
	}
	app.fileQuery = ""
	app.selectedFiles = map[string]bool{nativeSelectionKey(false, "a.go"): true}
	if got := app.checkedFiles(false); !slices.Equal(got, []string{"a.go"}) || !app.hasCheckedFiles(false) || app.hasCheckedFiles(true) {
		t.Fatalf("勾选结果=%v", got)
	}
	// 原地追加：首元素地址不变但长度变化，必须失效。
	files := make([]gitrepo.File, 0, 8)
	files = append(files, app.repository.snapshot.Files...)
	app.repository.snapshot.Files = files
	app.visibleFiles(false)
	app.repository.snapshot.Files = append(app.repository.snapshot.Files, gitrepo.File{Path: "d.go", Index: " ", Working: "M"})
	if got := paths(app.visibleFiles(false)); !slices.Equal(got, []string{"b.go", "a.go", "d.go"}) {
		t.Fatalf("快照追加后未重新过滤: %v", got)
	}
	app.repository = &repositoryView{}
	if got := app.visibleFiles(false); len(got) != 0 {
		t.Fatalf("替换仓库后仍返回旧文件: %v", paths(got))
	}
}

func TestNativeChangesTreeKeyboardNavigation(t *testing.T) {
	app, path, finish := nativeFixture(t)
	if err := os.Mkdir(filepath.Join(path, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dir/one.txt", "dir/two.txt", "top.txt"} {
		writeNativeFile(t, path, name, "一\n")
	}
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "目录")
	for _, name := range []string{"dir/one.txt", "dir/two.txt", "top.txt"} {
		writeNativeFile(t, path, name, "二\n")
	}
	app.refresh()
	finish()
	app.prefs.FileTree = true
	tester := ui.NewTester(app.view, 1200, 800)
	// 差异面板标题也含文件路径，这里只认目录树行里的文件名文本。
	shown := func(name string) bool { return slices.Contains(tester.Texts(), name) }
	if err := tester.Click("one.txt"); err != nil {
		t.Fatal(err)
	}
	finish()
	if app.selectedFile != "dir/one.txt" {
		t.Fatalf("点击文件后选中=%q", app.selectedFile)
	}
	tester.Key(0, ui.KeyDown)
	finish()
	if app.selectedFile != "dir/two.txt" {
		t.Fatalf("方向键下移后选中=%q", app.selectedFile)
	}
	// 文件上按左键回到父目录，再按左键折叠目录；选中文件保持不变。
	tester.Key(0, ui.KeyLeft)
	if !tester.Focused("未暂存目录 dir") {
		t.Fatal("文件上按左键没有回到父目录")
	}
	tester.Key(0, ui.KeyLeft)
	tester.Frame()
	if !app.fileCollapsed[nativeSelectionKey(false, "dir")] || shown("two.txt") {
		t.Fatal("左键没有折叠目录或未写回折叠状态")
	}
	if app.selectedFile != "dir/two.txt" {
		t.Fatalf("键盘停在目录上改变了选中文件: %q", app.selectedFile)
	}
	tester.Key(0, ui.KeyRight)
	tester.Frame()
	if app.fileCollapsed[nativeSelectionKey(false, "dir")] || !shown("two.txt") {
		t.Fatal("右键没有展开目录")
	}
	// 鼠标点击目录折叠，再次点击展开。
	if err := tester.Click("dir"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if shown("one.txt") {
		t.Fatal("点击目录没有折叠")
	}
	// 拉开时间，避免两次单击被识别为双击。
	tester.Advance(time.Second)
	if err := tester.Click("dir"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !shown("one.txt") {
		t.Fatal("再次点击目录没有展开")
	}
	// 快照替换后目录树随之重建。
	files := slices.Clone(app.repository.snapshot.Files)
	files = append(files, gitrepo.File{Path: "dir/three.txt", Index: " ", Working: "M"})
	app.repository.snapshot.Files = files
	tester.Frame()
	if !shown("three.txt") {
		t.Fatal("快照变化后目录树未重建")
	}
}

func TestNativeDiffBuffersBuiltOnDemand(t *testing.T) {
	app := &nativeApp{prefs: &nativePreferences{}}
	text := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n"
	old := &gitrepo.FileContent{Path: "a.txt", Revision: "HEAD", Text: "old\n", Present: true}
	next := &gitrepo.FileContent{Path: "a.txt", Revision: "WORKTREE", Text: "new\n", Present: true}
	app.setDiff(gitrepo.Diff{Text: text, Old: old, New: next})
	if len(app.diffViews) != 1 || app.rawDiff.Buffer != nil {
		t.Fatal("有结构化差异时不应构建原始差异 Buffer")
	}
	if app.fileViewer.states[0].Buffer != nil || app.fileViewer.states[1].Buffer != nil {
		t.Fatal("未打开全文时不应构建全文 Buffer")
	}
	buffer := app.fileViewer.state(1).Buffer
	if buffer == nil || buffer.String() != "new\n" || app.fileViewer.state(1).Buffer != buffer {
		t.Fatal("全文 Buffer 应在首次访问时构建且保持")
	}
	if app.fileViewer.states[0].Buffer != nil {
		t.Fatal("只访问新版本时不应构建旧版本")
	}
	same := *next
	app.setDiff(gitrepo.Diff{Text: text, Old: old, New: &same})
	if app.fileViewer.state(1).Buffer != buffer {
		t.Fatal("内容不变的版本重建了编辑器状态")
	}
	changed := *next
	changed.Text = "newer\n"
	app.setDiff(gitrepo.Diff{Text: text, Old: old, New: &changed})
	if got := app.fileViewer.state(1).Buffer; got == buffer || got.String() != "newer\n" {
		t.Fatal("版本内容变化后未重建全文")
	}
	var many strings.Builder
	for index := range nativeMaxDiffViews + 1 {
		fmt.Fprintf(&many, "diff --git a/f%d b/f%d\n--- a/f%d\n+++ b/f%d\n@@ -1 +1 @@\n-a\n+b\n", index, index, index, index)
	}
	app.setDiff(gitrepo.Diff{Text: many.String()})
	if len(app.diffViews) != 0 || app.rawDiff.Buffer == nil || app.rawDiff.Buffer.String() != many.String() {
		t.Fatal("文件过多时应退回原始差异")
	}
}

func TestNativeReferenceSidebarCache(t *testing.T) {
	app, _, _ := nativeFixture(t)
	tags := make([]gitrepo.Tag, 1, 4)
	tags[0] = gitrepo.Tag{Name: "v1"}
	app.repository.tags = tags
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { app.tagSidebar(c) })
	}, 400, 400)
	if tester.HasText("v1") {
		t.Fatal("标签默认应折叠")
	}
	if err := tester.Click("标签"); err != nil {
		t.Fatal(err)
	}
	if !tester.HasText("v1") {
		t.Fatal("展开后缺少标签")
	}
	// 原地追加：首地址不变，长度变化。
	app.repository.tags = append(app.repository.tags, gitrepo.Tag{Name: "v2"})
	tester.Frame()
	if !tester.HasText("v2") {
		t.Fatal("追加标签后节点未重建")
	}
	app.repository.tags = []gitrepo.Tag{{Name: "v3"}}
	tester.Frame()
	if !tester.HasText("v3") || tester.HasText("v1") {
		t.Fatal("替换标签后节点未重建")
	}
	app.comparison = &nativeComparison{}
	references := app.comparisonReferences()
	if !slices.Contains(references, "refs/tags/v3") {
		t.Fatalf("比较候选缺少标签: %v", references)
	}
	if again := app.comparisonReferences(); &again[0] != &references[0] {
		t.Fatal("引用未变化时比较候选应复用缓存")
	}
	app.repository.tags = []gitrepo.Tag{{Name: "v4"}}
	if references := app.comparisonReferences(); !slices.Contains(references, "refs/tags/v4") || slices.Contains(references, "refs/tags/v3") {
		t.Fatalf("标签变化后比较候选未更新: %v", references)
	}
}
