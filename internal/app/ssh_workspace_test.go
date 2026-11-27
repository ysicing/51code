// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func TestNativeRemoteLibrarySearchAndRemove(t *testing.T) {
	app, _, finish := nativeFixture(t)
	app.showLibrary()
	for _, repo := range []gitrepo.SSHWorkspace{{Host: "staging", Path: "/opt/web"}, {Host: "devbox", Path: "/srv/api"}} {
		var err error
		app.library, err = app.service.manager.RememberSSH(t.Context(), repo)
		if err != nil {
			t.Fatal(err)
		}
	}
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click(locales.RemoteRepositories()); err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); dir != "" {
		saveNativeScreenshot(t, tester, dir, "remote-repositories.png")
		tester.SetDark(true)
		saveNativeScreenshot(t, tester, dir, "remote-repositories-dark.png")
	}
	if err := tester.Click(locales.SearchSshRepositories()); err != nil {
		t.Fatal(err)
	}
	tester.Type("STAGING")
	if !tester.HasText("web") || tester.HasText("api") {
		t.Fatal("主机搜索没有筛选仓库")
	}
	app.remoteQuery = "/srv/"
	tester.Frame()
	if !tester.HasText("api") || tester.HasText("web") {
		t.Fatal("路径搜索没有筛选仓库")
	}
	if err := tester.Click(locales.Manage() + " devbox · /srv/api"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(locales.RemoveFromListMenu()); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click(locales.Remove()); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if len(app.library.SSHRepositories) != 1 || !tester.HasText(locales.NoMatchingRepositories()) {
		t.Fatal("移除后列表未更新")
	}
	if err := tester.Click(locales.LocalRepositories()); err != nil {
		t.Fatal(err)
	}
	if app.remoteLibrary || !nativeLibrarySearchVisible(tester) {
		t.Fatal("切回本地仓库后未恢复本地列表")
	}
}

func TestNativeSSHWorkspaceWindow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SSH 替身需要 POSIX shell")
	}
	bin := t.TempDir()
	shim := "#!/bin/sh\nexport HOME=\"$SSH_TEST_REMOTE_HOME\"\nexec sh -s\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	checkSSHWorkspaceWindow(t, "devbox")
}

func checkSSHWorkspaceWindow(t *testing.T, host string) {
	t.Helper()
	desktop, fixture, updates, _ := desktopFixture(t)
	remoteHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(remoteHome, "sample-repo")
	if err := os.Rename(fixture, path); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".config", "projects", "sandbox", "tools"} {
		if err := os.Mkdir(filepath.Join(remoteHome, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_TEST_REMOTE_HOME", remoteHome)
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	config := "Host dev-preview " + host + "\nHost staging\n"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	writeNativeFile(t, path, "hello.txt", "staged\n")
	nativeGit(t, path, "add", "hello.txt")
	writeNativeFile(t, path, "hello.txt", "working\n")
	writeNativeFile(t, path, "other.txt", "new\n")
	library := ui.NewTester(desktop.library.view, 1024, 680)
	if err := library.Click(locales.RemoteRepositories()); err != nil {
		t.Fatal(err)
	}
	chooseHost := func(capture bool) *nativeDialog {
		t.Helper()
		library.Frame()
		if err := library.Click(locales.ConnectSshRepository()); err != nil {
			t.Fatal(err)
		}
		chooser := desktop.library.dialog
		if chooser == nil {
			t.Fatal("SSH 主机选择器未打开")
		}
		runBackgroundUpdates(t, updates, func() bool { return chooser.cancel == nil })
		library.Frame()
		if dir := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); capture && dir != "" {
			saveNativeScreenshot(t, library, dir, "ssh-host-picker.png")
		}
		library.Type("DEV")
		if !library.HasText(host) || !library.HasText("dev-preview") || library.HasText("staging") {
			t.Fatal("输入应过滤已配置的 SSH 主机")
		}
		if dir := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); capture && dir != "" {
			library.SetDark(true)
			library.SetSize(640, 430)
			saveNativeScreenshot(t, library, dir, "ssh-host-picker-dark.png")
			library.SetSize(1024, 680)
			library.SetDark(false)
		}
		library.Key(0, ui.KeyDown)
		library.Key(0, ui.KeyEnter)
		form := desktop.library.dialog
		if form == nil || form == chooser || !library.HasText(host) {
			t.Fatal("方向键与回车应进入选中主机的仓库选择器")
		}
		return form
	}
	form := chooseHost(true)
	checkPath := func(want string) {
		t.Helper()
		library.Frame()
		field, found := library.AccessByRole(ui.RoleTextField, locales.SshWorkspacePath())
		if !found || field.Value != want || form.problem != "" {
			t.Fatalf("远端路径应为 %q，实际为 %q，错误 %q", want, field.Value, form.problem)
		}
	}
	runBackgroundUpdates(t, updates, func() bool { return form.cancel == nil })
	checkPath(remoteHome)
	library.SetSize(420, 430)
	library.SetDark(true)
	for _, name := range []string{locales.SshParentDirectory(), locales.BrowseSshDirectory(), locales.Open()} {
		button, found := library.AccessByRole(ui.RoleButton, name)
		if !found || button.Bounds.W <= 0 || button.Offscreen {
			t.Fatalf("紧凑窗口中缺少可用控件 %q", name)
		}
	}
	if dir := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); dir != "" {
		saveNativeScreenshot(t, library, dir, "ssh-repository-picker-dark.png")
	}
	library.SetSize(1024, 680)
	library.SetDark(false)
	if dir := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); dir != "" {
		saveNativeScreenshot(t, library, dir, "ssh-repository-picker.png")
	}
	if err := library.Click(filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
	runBackgroundUpdates(t, updates, func() bool { return form.cancel == nil })
	checkPath(path)
	if err := library.Click(locales.SshParentDirectory()); err != nil {
		t.Fatal(err)
	}
	runBackgroundUpdates(t, updates, func() bool { return form.cancel == nil })
	checkPath(remoteHome)
	if err := library.Click(filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
	runBackgroundUpdates(t, updates, func() bool { return form.cancel == nil })
	checkPath(path)
	if err := library.Click(locales.Open()); err != nil {
		t.Fatal(err)
	}
	runBackgroundUpdates(t, updates, func() bool { return desktop.library.dialog == nil || form.problem != "" })
	if form.problem != "" {
		t.Fatal(form.problem)
	}
	window := desktop.active
	if window == desktop.library || desktop.library.screen != "library" || window.repository != nil || window.ssh == nil {
		t.Fatal("远端仓库应使用独立窗口，并与本地 Git 操作隔离")
	}
	tester := ui.NewTester(window.view, 1024, 680)
	finish := func() {
		runBackgroundUpdates(t, updates, func() bool { return window.readTask == nil })
		tester.Frame()
		if window.problem != "" {
			t.Fatal(window.problem)
		}
	}
	finish()
	if !strings.Contains(window.diff.Text, "+working") {
		t.Fatalf("未显示工作区 Diff: %q", window.diff.Text)
	}
	if err := tester.Click(locales.FullFile()); err != nil {
		t.Fatal(err)
	}
	if !window.fileViewer.full || window.diff.Old == nil || window.diff.Old.Text != "staged\n" ||
		window.diff.New == nil || window.diff.New.Text != "working\n" {
		t.Fatal("远程工作区 Diff 未提供两侧全文")
	}
	if err := tester.Click("hello.txt"); err != nil {
		t.Fatal(err)
	}
	finish()
	if !window.staged || !strings.Contains(window.diff.Text, "+staged") {
		t.Fatalf("未打开暂存版本: %q", window.diff.Text)
	}
	if err := tester.Click(locales.FileDiff()); err != nil {
		t.Fatal(err)
	}
	window.navigateDiffHunk(0)
	tester.Frame()
	tester.Key(0, ui.KeyF7)
	if !tester.HasText(locales.NextDiffFileHint()) {
		t.Fatal("远程 Diff 的 F7 未提示下一个文件")
	}
	tester.Key(0, ui.KeyF7)
	finish()
	if window.staged || window.selectedFile != "hello.txt" || !strings.Contains(window.diff.Text, "+working") {
		t.Fatal("F7 未打开同名的未暂存变更")
	}
	window.fileQuery = "other"
	tester.Frame()
	if entries := window.orderedDiffFiles(); len(entries) != 1 || entries[0].path != "other.txt" {
		t.Fatalf("远程 Diff 导航未遵循文件筛选: %v", entries)
	}
	if err := tester.Click("other.txt"); err != nil {
		t.Fatal(err)
	}
	finish()
	if window.selectedFile != "other.txt" || !strings.Contains(window.diff.Text, "+new") {
		t.Fatal("未跟踪文件未按选择加载")
	}
	if dir := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); dir != "" {
		saveNativeScreenshot(t, tester, dir, "ssh-changes.png")
	}
	window.fileQuery = ""
	if err := tester.Click(locales.BrowseFile()); err != nil {
		t.Fatal(err)
	}
	finish()
	if window.screen != "files" || window.browser.file != "other.txt" || window.browser.viewer.sources[1].Text != "new\n" {
		t.Fatal("远程 Diff 未跳到工作区文件预览")
	}
	if err := tester.Click("hello.txt"); err != nil {
		t.Fatal(err)
	}
	finish()
	if source := window.browser.viewer.sources[1]; source == nil || source.Text != "working\n" {
		t.Fatal("远程文件浏览未打开工作区原文")
	}
	if dir := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); dir != "" {
		saveNativeScreenshot(t, tester, dir, "ssh-browse-files.png")
	}
	if err := tester.Click(locales.FileHistory()); err != nil {
		t.Fatal(err)
	}
	finish()
	if window.screen != "sshHistory" || window.ssh.historyFile != "hello.txt" || len(window.history) != 1 {
		t.Fatal("未打开远程文件历史")
	}
	if err := tester.Click(locales.Insights()); err != nil {
		t.Fatal(err)
	}
	finish()
	if window.screen != "insights" || window.insights.result == nil || window.insights.result.Commits != 1 {
		t.Fatal("未显示远程仓库洞察")
	}
	if err := tester.Click(locales.InsightsDays("7")); err != nil {
		t.Fatal(err)
	}
	finish()
	if len(window.insights.result.Days) != 7 {
		t.Fatal("洞察日期范围未更新")
	}
	if dir := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); dir != "" {
		saveNativeScreenshot(t, tester, dir, "ssh-insights.png")
	}
	window.prefs.SidebarItems.Insights = false
	window.reconcileSidebar()
	finish()
	if window.screen != "sshChanges" || tester.HasText(locales.Insights()) {
		t.Fatal("关闭洞察后应回到远程文件变更并隐藏入口")
	}
	if err := tester.Click(locales.CommitHistory()); err != nil {
		t.Fatal(err)
	}
	finish()
	if window.screen != "sshHistory" || len(window.history) != 1 || len(window.commitFiles) != 1 || len(window.diffViews) != 1 {
		t.Fatalf("提交图谱或逐文件 Diff 未加载: %s %d %d %d", window.screen, len(window.history), len(window.commitFiles), len(window.diffViews))
	}
	item, found := tester.AccessByRole(ui.RoleTreeItem, "main")
	if !found || item.Offscreen {
		t.Fatal("侧栏未显示远端工作区的 main 分支")
	}
	tester.ClickAt(item.Bounds.X+item.Bounds.W/2, item.Bounds.Y+item.Bounds.H/2)
	finish()
	if window.ssh.ref != "refs/heads/main" {
		t.Fatal("分支未筛选历史")
	}
	if err := tester.Click(locales.Refresh()); err != nil {
		t.Fatal(err)
	}
	finish()
	if err := tester.Click(locales.GoToChangedFile()); err != nil {
		t.Fatal(err)
	}
	tester.Type("hello.txt")
	tester.Key(0, ui.KeyEnter)
	finish()
	if window.commitFile != "hello.txt" || window.dialog != nil {
		t.Fatal("远程提交未使用变更文件查找")
	}
	if err := tester.Click(locales.Search()); err != nil {
		t.Fatal(err)
	}
	window.historyQuery = "初始"
	window.executeSearch()
	finish()
	if !window.historyLoaded || window.historyResult.query != "初始" || len(window.history) != 1 {
		t.Fatal("远程提交搜索未执行")
	}
	if err := tester.Click(locales.CommitHistory()); err != nil {
		t.Fatal(err)
	}
	finish()
	if dir := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); dir != "" {
		tester.SetDark(true)
		saveNativeScreenshot(t, tester, dir, "ssh-history.png")
	}
	if err := tester.Click(locales.BackToRepositories()); err != nil {
		t.Fatal(err)
	}
	if desktop.active != desktop.library {
		t.Fatal("返回入口未激活仓库库")
	}
	library.Frame()
	if err := library.Click(filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
	if desktop.active != window || len(desktop.repositories) != 1 || len(desktop.library.library.SSHRepositories) != 1 {
		t.Fatal("重复打开远端路径应复用原窗口")
	}
	if err := tester.Click(locales.BackToRepositories()); err != nil {
		t.Fatal(err)
	}
	form = chooseHost(false)
	if form.cancel != nil {
		t.Fatal("选中已有记录的主机应先显示已保存仓库")
	}
	if err := library.Click(path); err != nil {
		t.Fatal(err)
	}
	if desktop.library.dialog != nil || desktop.active != window {
		t.Fatal("点击已保存仓库应直接回到原窗口")
	}
}

func TestNativeSSHMergeParentComparison(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SSH 替身需要 POSIX shell")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexec sh -s\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	app, path, nextUpdate := nativeFixture(t)
	finish := func() {
		t.Helper()
		for app.readTask != nil {
			nextUpdate()
		}
		if app.problem != "" {
			t.Fatal(app.problem)
		}
	}
	nativeGit(t, path, "switch", "-c", "topic")
	writeNativeFile(t, path, "topic.txt", "topic change\n")
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "topic")
	second := nativeGit(t, path, "rev-parse", "HEAD")
	nativeGit(t, path, "switch", "main")
	writeNativeFile(t, path, "hello.txt", "main change\n")
	nativeGit(t, path, "commit", "-am", "main")
	first := nativeGit(t, path, "rev-parse", "HEAD")
	nativeGit(t, path, "merge", "--no-ff", "topic")
	merge := nativeGit(t, path, "rev-parse", "HEAD")
	nativeGit(t, path, "tag", "merged", merge)
	repo := gitrepo.SSHWorkspace{Host: "fixture", Path: path}
	snapshot, refs, err := repo.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	app.repository, app.writable = nil, false
	app.ssh = &nativeSSHWorkspace{repo: repo}
	app.ssh.update(snapshot, refs)
	app.startSSHHistory("", "")
	finish()
	tester := ui.NewTester(app.view, 1024, 680)
	if app.commitHash != merge || app.commitFile != "topic.txt" || len(app.commitFiles) != 1 ||
		app.diff.Old == nil || app.diff.Old.Revision != first {
		t.Fatalf("默认父提交的文件或 Diff 不正确: %v %+v", app.commitFiles, app.diff)
	}
	if err := tester.Click(locales.ComparisonBase() + " · " + shortHash(first)); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("2 · " + shortHash(second)); err != nil {
		t.Fatal(err)
	}
	finish()
	if app.commitParent != second || app.commitFile != "hello.txt" || len(app.commitFiles) != 1 ||
		app.diff.Old == nil || app.diff.Old.Revision != second || app.diff.New == nil || app.diff.New.Text != "main change\n" {
		t.Fatalf("切换第二父提交未更新文件或 Diff: %v %+v", app.commitFiles, app.diff)
	}
	if err := tester.Click(shortHash(first)); err != nil {
		t.Fatal(err)
	}
	finish()
	if app.commitHash != first || app.commitParent != "" {
		t.Fatal("点击父提交 ID 未切换历史")
	}
	app.switchSSH("refs/tags/merged")
	finish()
	if app.commitHash != merge || app.ssh.ref != "refs/tags/merged" {
		t.Fatal("标签入口未加载对应提交")
	}
	app.compareCommitWithHead(first)
	finish()
	if app.comparison == nil || app.comparison.result == nil || app.comparison.result.From != first ||
		app.comparison.result.Target != merge || app.comparison.file != "topic.txt" {
		t.Fatalf("未进入远程版本比较: %+v", app.comparison)
	}
}

func TestNativeSSHWorkspaceKeepsConnectionOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SSH 替身需要 POSIX shell")
	}
	bin := t.TempDir()
	shim := "#!/bin/sh\nprintf 'connection refused\\n' >&2\nexit 255\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	app, _, finish := nativeFixture(t)
	app.showLibrary()
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click(locales.RemoteRepositories()); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click(locales.ConnectSshRepository()); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	tester.Type("dev@manual")
	tester.Key(0, ui.KeyEnter)
	form := app.dialog
	if form == nil || !tester.HasText("dev@manual") {
		t.Fatal("没有 SSH 配置时仍应允许手动输入 user@host")
	}
	finish()
	tester.Frame()
	tester.Type("/srv/demo")
	tester.Key(0, ui.KeyEnter)
	finish()
	tester.Frame()
	field, found := tester.AccessByRole(ui.RoleTextField, locales.SshWorkspacePath())
	if app.dialog != form || form.problem == "" || form.blocked || !found || field.Value != "/srv/demo" {
		t.Fatal("连接失败后应保留可重试的连接表单和远端路径")
	}
	if err := tester.Click(locales.ChangeSshHost()); err != nil {
		t.Fatal(err)
	}
	finish()
	tester.Frame()
	if app.dialog == nil || app.dialog == form || !tester.HasText(locales.NoSshHosts()) {
		t.Fatal("更换主机应返回选择器并显示空状态")
	}
	tester.Key(0, ui.KeyEscape)
	if app.dialog != nil || app.screen != "library" {
		t.Fatal("关闭后未回到仓库列表")
	}
}
