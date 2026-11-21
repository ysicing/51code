// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func desktopFixture(t *testing.T) (*nativeDesktop, string, chan func(), *[]*nativeApp) {
	t.Helper()
	base, path, _ := nativeFixture(t)
	desktop := newNativeDesktop(base.service, base.stateFile)
	updates := make(chan func(), 64)
	var presented []*nativeApp
	desktop.showWindow = func(app *nativeApp) {
		if app.post == nil {
			app.post = func(update func()) { updates <- update }
			t.Cleanup(app.cancel)
		}
		presented = append(presented, app)
	}
	desktop.showLibrary()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return desktop, canonical, updates, &presented
}

func openDesktopRepository(t *testing.T, desktop *nativeDesktop, updates <-chan func(), path string) *nativeApp {
	t.Helper()
	desktop.library.openRepository(path)
	runBackgroundUpdates(t, updates, func() bool { return !desktop.library.busy })
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range desktop.repositories {
		if app.repository.snapshot.Path == canonical {
			return app
		}
	}
	t.Fatalf("未打开仓库 %s: %s", path, desktop.library.problem)
	return nil
}

func TestNativeBranchViewAndWorktreeCheckout(t *testing.T) {
	desktop, path, updates, _ := desktopFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	nativeGit(t, path, "worktree", "add", "-b", "occupied", linked)
	nativeGit(t, path, "branch", "available")
	writeNativeFile(t, linked, "hello.txt", "occupied commit\n")
	nativeGit(t, linked, "commit", "-am", "occupied tip")
	tip := nativeGit(t, linked, "rev-parse", "HEAD")
	nativeGit(t, path, "commit", "--allow-empty", "-m", "source only")
	head := nativeGit(t, path, "rev-parse", "HEAD")
	writeNativeFile(t, path, "hello.txt", "main draft\n")
	writeNativeFile(t, linked, "hello.txt", "linked draft\n")
	linkedPath, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	source := openDesktopRepository(t, desktop, updates, path)
	source.writable = false
	source.switchWorkspace("history")
	source.searchHistory(false)
	runBackgroundUpdates(t, updates, func() bool { return source.readTask == nil })
	allHistory := slices.Clone(source.history)
	historyList := source.historyList
	tester := ui.NewTester(source.view, 1024, 680)
	checkoutBranch := func(name string) {
		t.Helper()
		tester.Frame()
		if err := tester.RightClick(name); err != nil {
			t.Fatal(err)
		}
		if err := tester.ChooseMenuItem(locales.CheckoutBranchOrOpenWorktree()); err != nil {
			t.Fatal(err)
		}
		runBackgroundUpdates(t, updates, func() bool { return !source.busy })
		if source.problem != "" {
			t.Fatal(source.problem)
		}
	}
	if err := tester.Click("occupied"); err != nil {
		t.Fatal(err)
	}
	runBackgroundUpdates(t, updates, func() bool { return source.readTask == nil })
	if source.problem != "" || source.screen != "history" || source.historyRequest() != (nativeHistoryRequest{}) ||
		len(allHistory) != 3 || !slices.EqualFunc(source.history, allHistory, func(left, right gitrepo.Commit) bool { return left.Hash == right.Hash }) ||
		source.historyList != historyList || source.commitHash != tip || source.historySelected < 0 ||
		!strings.Contains(source.diff.Text, "occupied commit") || source.historyWorkingOffset() != 1 ||
		len(desktop.repositories) != 1 || source.repository.snapshot.Branch != "main" {
		t.Fatal("单击工作树中的分支未在原历史列表中定位提交和差异")
	}
	source.historyFilter.CurrentBranch = true
	source.searchHistory(false)
	runBackgroundUpdates(t, updates, func() bool { return source.readTask == nil })
	tester.Frame()
	if err := tester.Click("occupied"); err != nil {
		t.Fatal(err)
	}
	runBackgroundUpdates(t, updates, func() bool { return source.readTask == nil })
	tester.Frame()
	if !source.historyFilter.CurrentBranch || len(source.history) != 2 || source.history[0].Hash != head ||
		source.historySelected != -1 || source.commitHash != tip || !strings.Contains(source.diff.Text, "occupied commit") {
		t.Fatal("筛选外的分支定位应显示提交详情并保留历史筛选")
	}
	if err := tester.Click(locales.ViewInAllHistory()); err != nil {
		t.Fatal(err)
	}
	runBackgroundUpdates(t, updates, func() bool { return source.readTask == nil })
	if source.historyFilter.CurrentBranch || source.historySelected < 0 || source.commitHash != tip ||
		len(source.history) != len(allHistory) {
		t.Fatal("显式查看全部历史未定位分支尖端")
	}
	checkoutBranch("occupied")
	target := desktop.repositories[linkedPath]
	if target == nil || target.repository.snapshot.Branch != "occupied" || desktop.active != target {
		t.Fatal("已检出的分支未在对应工作树窗口打开")
	}
	if source.repository.snapshot.Branch != "main" || !fileExists(target.repository.snapshot.Files, "hello.txt", false) ||
		!fileExists(source.repository.snapshot.Files, "hello.txt", false) {
		t.Fatal("打开工作树改变了来源分支或丢失未提交的文件状态")
	}
	checkoutBranch("occupied")
	if desktop.repositories[linkedPath] != target || desktop.active != target || len(desktop.repositories) != 2 {
		t.Fatal("重复点击没有激活原来的工作树窗口")
	}
	source.writable = true
	checkoutBranch("available")
	if source.repository.snapshot.Branch != "available" ||
		nativeGit(t, linked, "branch", "--show-current") != "occupied" {
		t.Fatal("检出空闲分支未在来源工作树切换，或影响了另一个工作树")
	}
}

func TestNativeDesktopRepositoryWindowsKeepIndependentState(t *testing.T) {
	desktop, path, updates, _ := desktopFixture(t)
	_, otherPath, _ := nativeFixture(t)
	library := desktop.library
	library.query = "保持搜索"
	first := openDesktopRepository(t, desktop, updates, path)
	first.screen, first.selectedFile, first.fileQuery = "history", "hello.txt", "hello"
	first.historyOffset = 150
	first.prefs.Drafts[path] = "第一个仓库的草稿"
	second := openDesktopRepository(t, desktop, updates, otherPath)
	second.prefs.Drafts[otherPath] = "第二个仓库的草稿"
	library.openRepository(path)
	runBackgroundUpdates(t, updates, func() bool { return !library.busy })
	if desktop.prefs.LastRepository != path {
		t.Fatal("重新打开已有窗口也应更新下次启动恢复的仓库")
	}
	if library.repository != nil || library.screen != "library" || library.query != "保持搜索" ||
		first.screen != "history" || first.selectedFile != "hello.txt" || first.fileQuery != "hello" || first.historyOffset != 150 {
		t.Fatal("打开另一个仓库改变了原窗口或列表状态")
	}
	if first == second || first.service == second.service || first.ctx == second.ctx ||
		first.historyList == second.historyList || first.selectedFiles == nil || second.selectedFiles == nil {
		t.Fatal("仓库窗口没有隔离服务、上下文或阅读状态")
	}
	first.selectedFiles["hello.txt"] = true
	if second.selectedFiles["hello.txt"] {
		t.Fatal("窗口共享了文件选择")
	}
	first.showLibrary()
	if library.query != "保持搜索" || first.screen != "history" {
		t.Fatal("返回列表不应改变仓库窗口或列表筛选")
	}
	if first.prefs != second.prefs || first.prefs != library.prefs {
		t.Fatal("应用偏好必须由一个实例管理")
	}
	for _, app := range []*nativeApp{first, second, library} {
		if !app.prepareClose() {
			t.Fatal("无未保存编辑时应能独立关闭")
		}
	}
	restored := newNativeApp(first.service, first.stateFile)
	t.Cleanup(restored.cancel)
	if restored.prefs.Drafts[path] != "第一个仓库的草稿" || restored.prefs.Drafts[otherPath] != "第二个仓库的草稿" {
		t.Fatal("不同窗口保存时覆盖了其他仓库的草稿")
	}
	first.closed = true
	first.cancel()
	desktop.remove(first)
	if len(desktop.repositories) != 1 || second.ctx.Err() != nil || library.ctx.Err() != nil {
		t.Fatal("关闭单个仓库影响了其他窗口")
	}
	library.closed = true
	library.cancel()
	desktop.remove(library)
	reopened := desktop.showLibrary()
	if reopened == library || reopened.prefs != second.prefs || second.ctx.Err() != nil {
		t.Fatal("重新打开列表没有保留共享设置或影响了仓库")
	}
}

func TestNativeDesktopCanonicalPathsReuseWindowAndWorktreesStaySeparate(t *testing.T) {
	desktop, path, updates, presented := desktopFixture(t)
	first := openDesktopRepository(t, desktop, updates, path)
	nested := filepath.Join(path, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	desktop.library.openRepository(nested)
	runBackgroundUpdates(t, updates, func() bool { return !desktop.library.busy })
	if len(desktop.repositories) != 1 || (*presented)[len(*presented)-1] != first {
		t.Fatal("从子目录打开同一仓库应激活已有窗口")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(path, alias); err == nil {
		desktop.library.openRepository(alias)
		runBackgroundUpdates(t, updates, func() bool { return !desktop.library.busy })
		if len(desktop.repositories) != 1 || (*presented)[len(*presented)-1] != first {
			t.Fatal("通过符号链接打开同一仓库应激活已有窗口")
		}
	}
	worktree := filepath.Join(t.TempDir(), "linked")
	nativeGit(t, path, "worktree", "add", "-b", "linked", worktree)
	second := openDesktopRepository(t, desktop, updates, worktree)
	if first == second || len(desktop.repositories) != 2 || repositoryWindowKey(path) == repositoryWindowKey(worktree) {
		t.Fatal("不同工作树应各自拥有窗口与窗口位置")
	}
	first.busy, first.status = true, "正在提交"
	count := len(*presented)
	desktop.library.openRepository(path)
	if len(*presented) != count+1 || (*presented)[count] != first || !first.busy {
		t.Fatal("重复打开正在写入的仓库应只激活窗口")
	}
}

func TestNativeDesktopOpenFailureAndClosedSourceDoNotCreateWindows(t *testing.T) {
	desktop, _, updates, _ := desktopFixture(t)
	library := desktop.library
	library.openRepository(t.TempDir())
	runBackgroundUpdates(t, updates, func() bool { return !library.busy })
	if library.problem == "" || len(desktop.repositories) != 0 || library.screen != "library" {
		t.Fatal("打开失败应保留列表并显示错误")
	}
	started := make(chan struct{})
	library.runTask("读取仓库", true, func(ctx context.Context) (func(), error) {
		close(started)
		<-ctx.Done()
		return func() { t.Error("窗口关闭后不应回填") }, ctx.Err()
	})
	<-started
	if !library.prepareClose() {
		t.Fatal("可取消的读取不应阻止关闭")
	}
	library.closed = true
	library.cancel()
	select {
	case update := <-updates:
		update()
	case <-time.After(10 * time.Second):
		t.Fatal("取消后后台读取未退出")
	}
	if len(desktop.repositories) != 0 {
		t.Fatal("已关闭窗口的迟到结果创建了仓库窗口")
	}
}

func TestNativeDesktopConcurrentOpensShareOneRequest(t *testing.T) {
	desktop, path, updates, _ := desktopFixture(t)
	first := openDesktopRepository(t, desktop, updates, path)
	_, otherPath, _ := nativeFixture(t)
	canonical, err := filepath.EvalSymlinks(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	desktop.openCanonicalRepository(desktop.library, canonical)
	request := desktop.opening[canonical]
	desktop.openCanonicalRepository(first, canonical)
	if request == nil || desktop.opening[canonical] != request || first.busy {
		t.Fatal("相同仓库的并发打开应复用已有请求")
	}
	runBackgroundUpdates(t, updates, func() bool { return !desktop.library.busy })
	if len(desktop.repositories) != 2 || len(desktop.opening) != 0 {
		t.Fatal("并发打开创建了重复窗口或遗留请求")
	}
}

func TestNativeDesktopReopenWaitsForClosedRequestToFinish(t *testing.T) {
	desktop, path, updates, _ := desktopFixture(t)
	previous := desktop.library
	done := make(chan struct{})
	desktop.opening[path] = &nativeRepositoryOpening{source: previous, done: done}
	previous.closed = true
	previous.cancel()
	desktop.remove(previous)
	current := desktop.showLibrary()
	desktop.openCanonicalRepository(current, path)
	if !current.busy || len(desktop.repositories) != 0 {
		t.Fatal("旧请求未退出时不应创建仓库窗口")
	}
	close(done)
	runBackgroundUpdates(t, updates, func() bool { return !current.busy })
	if len(desktop.repositories) != 1 || len(desktop.opening) != 0 {
		t.Fatal("旧请求退出后应完成重新打开并清理请求")
	}
}

func TestNativeDesktopQuitChecksEveryWindowBeforeClosing(t *testing.T) {
	desktop, path, updates, _ := desktopFixture(t)
	repository := openDesktopRepository(t, desktop, updates, path)
	library := desktop.library
	prompts := 0
	repository.closeMessage = func(mygo.MessageOptions) (mygo.MessageResult, error) {
		prompts++
		return mygo.MessageResult{Button: 0}, nil
	}
	repository.busy, repository.status = true, "正在推送"
	if desktop.prepareQuit() || desktop.quitting || prompts != 1 || library.closed || repository.ctx.Err() != nil {
		t.Fatal("任意窗口写入期间都必须取消整次退出")
	}
	repository.busy = false
	repository.aiEditor = &nativeAISettings{key: "未保存密钥"}
	if desktop.prepareQuit() || desktop.quitting || prompts != 2 || library.closed || repository.closed {
		t.Fatal("取消丢弃未保存密钥时所有窗口都应保留")
	}
	repository.aiEditor = nil
	repository.conflict = &gitrepo.Conflict{Result: "原始结果"}
	repository.conflictEdit.Buffer = editor.NewBuffer("未保存的冲突结果")
	if desktop.prepareQuit() || desktop.quitting || !repository.conflictDirty() || library.closed {
		t.Fatal("取消关闭时必须保留冲突编辑与全部窗口")
	}
	repository.conflict = nil
	previous := library.stateFile
	library.stateFile = filepath.Join(t.TempDir(), "阻止保存", "state.json")
	if err := os.WriteFile(filepath.Dir(library.stateFile), nil, 0600); err != nil {
		t.Fatal(err)
	}
	library.closeMessage = func(mygo.MessageOptions) (mygo.MessageResult, error) {
		return mygo.MessageResult{}, nil
	}
	if desktop.prepareQuit() || desktop.quitting || repository.closed {
		t.Fatal("保存失败必须保留全部窗口")
	}
	library.stateFile = previous
	if !desktop.prepareQuit() || !desktop.quitting {
		t.Fatal("所有窗口允许关闭后才能进入退出阶段")
	}
}

func TestNativeDesktopCommandsTargetTheirWindowAndQuitCannotReenter(t *testing.T) {
	desktop, path, updates, _ := desktopFixture(t)
	repository := openDesktopRepository(t, desktop, updates, path)
	library := desktop.library
	library.win, repository.win = &mygo.Window{}, &mygo.Window{}
	if desktop.target(library.win) != library || desktop.target(repository.win) != repository ||
		desktop.target(&mygo.Window{}) != nil {
		t.Fatal("菜单命令没有按所属窗口定位")
	}
	desktop.active = repository
	if desktop.target(nil) != repository {
		t.Fatal("菜单回调缺少焦点窗口时应使用最后激活的窗口")
	}
	library.win, repository.win = nil, nil
	repository.aiEditor = &nativeAISettings{key: "保留密钥"}
	repository.closeMessage = func(mygo.MessageOptions) (mygo.MessageResult, error) {
		if desktop.prepareQuit() {
			t.Error("确认期间不应允许重新进入退出流程")
		}
		library.run("禁止启动", func(context.Context) (func(), error) {
			t.Error("退出预检期间不应启动新的写入")
			return nil, nil
		})
		if library.busy {
			t.Error("退出预检期间不应接受新任务")
		}
		library.settingsDialog()
		if library.dialog != nil {
			t.Error("退出预检期间不应打开新的设置草稿")
		}
		return mygo.MessageResult{Button: 0}, nil
	}
	if desktop.prepareQuit() || desktop.checkingQuit || desktop.quitting {
		t.Fatal("取消退出后必须清除预检状态")
	}
}

func TestNativeDesktopGitSelectionWaitsForOtherWindowWrites(t *testing.T) {
	desktop, path, updates, _ := desktopFixture(t)
	repository := openDesktopRepository(t, desktop, updates, path)
	library := desktop.library
	dialog := &nativeDialog{}
	library.dialog = dialog
	identity := &nativeIdentityEditor{}
	repository.busy = true
	library.selectGitExecutable("system", "", identity, dialog)
	if library.busy {
		t.Fatal("其他窗口写入时不应开始切换全局 Git")
	}
	repository.busy = false
	library.selectGitExecutable("system", "", identity, dialog)
	if !library.busy {
		t.Fatal("空闲时应能检测新的 Git")
	}
	repository.busy = true
	runBackgroundUpdates(t, updates, func() bool { return !library.busy })
	if dialog.problem != locales.OperationBusyDetail() || identity.ready {
		t.Fatal("检测期间其他窗口开始写入时，不应应用全局 Git 切换")
	}
}

func TestNativeDesktopSharesSettingsAndProtectsSettingsDrafts(t *testing.T) {
	mygo.App.SetPath(mygo.PathUserData, t.TempDir())
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })
	settingsTestUpdater.Do(func() {
		mygo.Use(native.New(updater.Options{DisableAutomaticChecks: true, Language: "zh-Hans"}))
	})
	desktop, path, updates, presented := desktopFixture(t)
	repository := openDesktopRepository(t, desktop, updates, path)
	library := desktop.library
	library.settingsDialog()
	ui.NewTester(library.view, 1024, 680).Frame()
	library.aiEditor.key = "尚未保存"
	count := len(*presented)
	repository.settingsDialog()
	if len(*presented) != count+1 || (*presented)[count] != library ||
		repository.dialog != nil || library.aiEditor.key != "尚未保存" {
		t.Fatal("另一个窗口打开应用设置应定位原设置并保留草稿")
	}
	library.prefs.Appearance = appearanceDark
	if err := library.savePreferences(); err != nil {
		t.Fatal(err)
	}
	if repository.prefs.Appearance != appearanceDark {
		t.Fatal("应用设置没有同步到仓库窗口")
	}
	repository.screen = "search"
	library.prefs.SidebarItems.Search = false
	if err := library.savePreferences(); err != nil {
		t.Fatal(err)
	}
	if repository.screen != "changes" {
		t.Fatal("从其他窗口隐藏侧栏入口后应回到文件变更")
	}
	repository.screen, repository.busy = "search", true
	repository.reconcileSidebar()
	if repository.screen != "search" {
		t.Fatal("其他窗口的设置变化不应打断正在执行的操作")
	}
	repository.busy = false
	repository.reconcileSidebar()
	if repository.screen != "changes" {
		t.Fatal("任务完成后应退出已隐藏的页面")
	}
	library.dialog = nil
	library.aiEditor = nil
	repository.settingsDialog()
	if repository.dialog == nil || repository.dialog.title != locales.ApplicationSettings() {
		t.Fatal("关闭原设置后应能在当前窗口打开设置")
	}
}
