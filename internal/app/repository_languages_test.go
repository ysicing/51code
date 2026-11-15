// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/internal/workspace"

	"github.com/egoist/mygo/ui"
)

func TestRepositoryLanguageCacheReusesTreeAndRecoversCorruption(t *testing.T) {
	app, path, _ := nativeFixture(t)
	cache := t.TempDir()
	writeNativeFile(t, path, "main.go", "package main\n")
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "Go")
	summary, err := readRepositoryLanguages(context.Background(), path, cache)
	if err != nil || len(summary.Languages) != 1 || summary.Languages[0].Name != "Go" {
		t.Fatalf("initial languages = %+v, %v", summary, err)
	}
	file := repositoryLanguageCachePath(cache, path)
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	nativeGit(t, path, "commit", "--allow-empty", "-m", "same tree")
	if _, err := readRepositoryLanguages(context.Background(), path, cache); err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.Stat(file)
	if err != nil || !unchanged.ModTime().Equal(info.ModTime()) {
		t.Fatalf("matching tree rewrote cache: %v", err)
	}
	nativeGit(t, path, "rm", "main.go")
	writeNativeFile(t, path, "main.py", "print('hello')\n")
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "Python")
	changed, err := readRepositoryLanguages(context.Background(), path, cache)
	if err != nil || changed.Tree == summary.Tree || len(changed.Languages) != 1 || changed.Languages[0].Name != "Python" {
		t.Fatalf("changed tree = %+v, %v", changed, err)
	}
	for _, invalid := range []string{`{`, `{"version":"old","summary":{}}`} {
		if err := os.WriteFile(file, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		recovered, err := readRepositoryLanguages(context.Background(), path, cache)
		if err != nil || len(recovered.Languages) != 1 || recovered.Languages[0].Name != "Python" {
			t.Fatalf("corrupt cache = %+v, %v", recovered, err)
		}
	}
	if app.busy || app.problem != "" {
		t.Fatal("language analysis changed foreground operation state")
	}
	if err := os.Rename(path, path+"-offline"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(path+"-offline", path) })
	if _, err := readRepositoryLanguages(context.Background(), path, cache); err == nil {
		t.Fatal("unavailable repository did not return a read error")
	}
	if fallback, found := loadRepositoryLanguageCache(cache, path); !found ||
		len(fallback.Languages) != 1 || fallback.Languages[0].Name != "Python" {
		t.Fatalf("unavailable repository lost cached label: %+v", fallback)
	}
}

func TestRepositoryLanguagesCacheFailureAndUnbornRepository(t *testing.T) {
	_, path, _ := nativeFixture(t)
	writeNativeFile(t, path, "main.go", "package main\n")
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "Go")
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	summary, err := readRepositoryLanguages(context.Background(), path, blocked)
	if err != nil || len(summary.Languages) != 1 {
		t.Fatalf("cache write failure lost analysis: %+v, %v", summary, err)
	}
	empty := t.TempDir()
	nativeGit(t, empty, "init", "-b", "main")
	summary, err = readRepositoryLanguages(context.Background(), empty, t.TempDir())
	if err != nil || summary.Tree != "" || len(summary.Languages) != 0 {
		t.Fatalf("unborn repository = %+v, %v", summary, err)
	}
}

func TestNativeLanguageWorkerRejectsSupersededAndClosedResults(t *testing.T) {
	app, path, _ := nativeFixture(t)
	writeNativeFile(t, path, "main.go", "package main\n")
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "Go")
	app.screen = "library"
	updates := make(chan func(), 32)
	app.post = func(update func()) { updates <- update }
	app.languages = &nativeRepositoryLanguages{cacheDir: t.TempDir(), entries: map[string]gitrepo.LanguageSummary{}}
	app.refreshRepositoryLanguages(false)
	first := app.languages.request
	select {
	case <-first.done:
	case <-time.After(10 * time.Second):
		t.Fatal("language worker did not finish")
	}
	app.library.Repositories = nil
	app.refreshRepositoryLanguages(false)
	second := app.languages.request
	select {
	case <-second.done:
	case <-time.After(10 * time.Second):
		t.Fatal("replacement worker did not finish")
	}
	for len(updates) > 0 {
		(<-updates)()
	}
	if len(app.languages.entries) != 0 || app.busy || app.problem != "" {
		t.Fatal("removed repository received stale language result")
	}
	app.library.Repositories = []workspace.LocalRepository{{Path: path, Name: "test"}}
	app.refreshRepositoryLanguages(false)
	select {
	case <-app.languages.request.done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not finish")
	}
	app.closed = true
	for len(updates) > 0 {
		(<-updates)()
	}
	if len(app.languages.entries) != 0 {
		t.Fatal("closed window received language result")
	}
}

func TestNativeRepositoryLanguageLayout(t *testing.T) {
	app, path, _ := nativeFixture(t)
	app.screen = "library"
	app.library.Repositories[0].Name = strings.Repeat("长仓库名称", 12)
	app.library.Repositories[0].Path = path
	app.library.Repositories[0].Branch = strings.Repeat("feature/long-branch/", 12)
	summary := gitrepo.LanguageSummary{Complete: true, Languages: []gitrepo.LanguageStat{
		{Name: "Go", Color: "#00ADD8", Bytes: 80},
		{Name: "Python", Color: "#3572A5", Bytes: 15},
		{Name: "Shell", Color: "#89e051", Bytes: 5},
	}}
	app.languages = &nativeRepositoryLanguages{entries: map[string]gitrepo.LanguageSummary{path: summary}}
	for _, appearance := range []string{appearanceLight, appearanceDark} {
		app.prefs.Appearance = appearance
		for _, width := range []int{1024, 1320} {
			tester := ui.NewTester(app.view, width, 860)
			if tester.HasText("Go") != (width == 1320) {
				t.Fatalf("language visibility: width=%d appearance=%s", width, appearance)
			}
			if width == 1320 {
				bounds, found := tester.Find("Go")
				if !found || bounds.X < app.prefs.Sidebar || bounds.X+bounds.W > float32(width) {
					t.Fatalf("language clipped: %+v", bounds)
				}
			}
			row, found := tester.AccessByRole(ui.RoleButton, app.library.Repositories[0].Name)
			if !found || row.Description != "Go 80.0% · Python 15.0% · Shell 5.0%" {
				t.Fatalf("repository row has no accessible language summary: %+v", row)
			}
			if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
				saveNativeScreenshot(t, tester, directory, fmt.Sprintf("native-repository-languages-%s-%d.png", appearance, width))
			}
		}
	}
	if got := repositoryLanguageTooltip(summary); got != "Go 80.0% · Python 15.0% · Shell 5.0%" {
		t.Fatalf("tooltip = %q", got)
	}
	summary.Complete = false
	if got := repositoryLanguageTooltip(summary); strings.Contains(got, "%") || got != "Go · Python · Shell" {
		t.Fatalf("partial analysis claims exact percentages: %q", got)
	}
	delete(app.languages.entries, path)
	if tester := ui.NewTester(app.view, 1320, 860); tester.HasText("Go") {
		t.Fatal("unknown repository shows a language placeholder")
	}
}

func TestNativeLanguageRefreshKeepsPredecessorBarrier(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.screen = "library"
	app.library.Repositories = nil
	blocked := &nativeLanguageRequest{cancel: func() {}, done: make(chan struct{})}
	app.languages = &nativeRepositoryLanguages{entries: map[string]gitrepo.LanguageSummary{}, request: blocked}
	app.refreshRepositoryLanguages(true)
	second := app.languages.request
	app.refreshRepositoryLanguages(true)
	third := app.languages.request
	select {
	case <-second.done:
		t.Error("cancelled intermediate request retired before its predecessor")
	case <-time.After(200 * time.Millisecond):
	}
	close(blocked.done)
	select {
	case <-third.done:
	case <-time.After(5 * time.Second):
		t.Fatal("latest request did not resume after predecessor exited")
	}
}

func TestNativeLanguageDiskFallbackDoesNotReplaceNewerMemory(t *testing.T) {
	app, _, _ := nativeFixture(t)
	path := app.repository.snapshot.Path
	cache := t.TempDir()
	writeNativeFile(t, path, "main.go", "package main\n")
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "Go")
	if _, err := readRepositoryLanguages(context.Background(), path, cache); err != nil {
		t.Fatal(err)
	}
	nativeGit(t, path, "rm", "main.go")
	writeNativeFile(t, path, "main.py", "print('hello')\n")
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "Python")
	newer, err := readRepositoryLanguages(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+"-offline"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(path+"-offline", path) })
	app.screen = "library"
	app.languages = &nativeRepositoryLanguages{cacheDir: cache, entries: map[string]gitrepo.LanguageSummary{path: newer}}
	updates := make(chan func(), 8)
	app.post = func(update func()) { updates <- update }
	app.refreshRepositoryLanguages(false)
	select {
	case <-app.languages.request.done:
	case <-time.After(5 * time.Second):
		t.Fatal("offline lookup did not return")
	}
	for len(updates) > 0 {
		(<-updates)()
	}
	if got := app.languages.entries[path]; got.Tree != newer.Tree || len(got.Languages) != 1 || got.Languages[0].Name != "Python" {
		t.Fatalf("older disk cache replaced successful in-memory analysis: %+v", got)
	}
}

func TestNativeLanguageWorkerHydratesCachesBeforeResourceWait(t *testing.T) {
	app, _, _ := nativeFixture(t)
	path := app.repository.snapshot.Path
	writeNativeFile(t, path, "main.go", "package main\n")
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "Go")
	second := filepath.Join(t.TempDir(), "second")
	nativeGit(t, path, "clone", "--local", path, second)
	second, err := filepath.EvalSymlinks(second)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	for _, root := range []string{path, second} {
		if _, err := readRepositoryLanguages(context.Background(), root, cache); err != nil {
			t.Fatal(err)
		}
	}
	nativeGit(t, second, "config", "user.name", "Language Test")
	nativeGit(t, second, "config", "user.email", "language@example.test")
	nativeGit(t, second, "config", "commit.gpgsign", "false")
	nativeGit(t, second, "rm", "main.go")
	writeNativeFile(t, second, "main.swift", "print(\"hello\")\n")
	nativeGit(t, second, "add", ".")
	nativeGit(t, second, "commit", "-m", "Swift")
	app.library.Repositories = []workspace.LocalRepository{{Path: path}, {Path: second}}
	app.screen, app.problem = "library", "existing operation error"
	updates := make(chan func(), 16)
	app.post = func(update func()) { updates <- update }
	waiting, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	app.languages = &nativeRepositoryLanguages{
		cacheDir: cache, entries: map[string]gitrepo.LanguageSummary{},
		waitForResources: func(ctx context.Context) error {
			once.Do(func() { close(waiting) })
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		},
	}
	app.refreshRepositoryLanguages(false)
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not consult resource gate")
	}
	for len(updates) > 0 {
		(<-updates)()
	}
	for _, root := range []string{path, second} {
		if got := app.languages.entries[root]; len(got.Languages) != 1 || got.Languages[0].Name != "Go" {
			t.Fatalf("resource wait blocked cached label for %s: %+v", root, got)
		}
	}
	if app.busy || app.problem != "existing operation error" {
		t.Fatal("resource wait changed foreground operation state")
	}
	close(release)
	select {
	case <-app.languages.request.done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not resume after resource gate opened")
	}
	for len(updates) > 0 {
		(<-updates)()
	}
	if got := app.languages.entries[second]; len(got.Languages) != 1 || got.Languages[0].Name != "Swift" {
		t.Fatalf("resource recovery did not refresh cached language: %+v", got)
	}
}

func TestNativeLanguageWorkerKeepsCacheWhenResourceSamplingFails(t *testing.T) {
	app, _, _ := nativeFixture(t)
	path := app.repository.snapshot.Path
	cache := t.TempDir()
	writeNativeFile(t, path, "main.go", "package main\n")
	nativeGit(t, path, "add", ".")
	nativeGit(t, path, "commit", "-m", "Go")
	if _, err := readRepositoryLanguages(context.Background(), path, cache); err != nil {
		t.Fatal(err)
	}
	updates := make(chan func(), 16)
	app.post = func(update func()) { updates <- update }
	app.screen = "library"
	app.languages = &nativeRepositoryLanguages{
		cacheDir: cache, entries: map[string]gitrepo.LanguageSummary{},
		waitForResources: func(context.Context) error { return errors.New("unavailable") },
	}
	app.refreshRepositoryLanguages(false)
	select {
	case <-app.languages.request.done:
	case <-time.After(5 * time.Second):
		t.Fatal("failed sampling did not end refresh")
	}
	for len(updates) > 0 {
		(<-updates)()
	}
	if got := app.languages.entries[path]; len(got.Languages) != 1 || got.Languages[0].Name != "Go" || app.problem != "" || app.busy {
		t.Fatalf("sampling failure lost cache or affected foreground: %+v", got)
	}
}

func TestNativeLanguageRefreshCoalescesAndThrottles(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.screen = "library"
	updates := make(chan func(), 32)
	app.post = func(update func()) { updates <- update }
	waiting, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	app.languages = &nativeRepositoryLanguages{
		cacheDir: t.TempDir(), entries: map[string]gitrepo.LanguageSummary{},
		waitForResources: func(ctx context.Context) error {
			once.Do(func() { close(waiting) })
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		},
	}
	app.refreshRepositoryLanguages(false)
	first := app.languages.request
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("language worker did not start")
	}
	for range 5 {
		app.refreshRepositoryLanguages(false)
		if app.languages.request != first {
			t.Fatal("automatic refresh cancelled an active language worker")
		}
	}
	close(release)
	select {
	case <-first.done:
	case <-time.After(5 * time.Second):
		t.Fatal("language worker did not finish")
	}
	app.refreshRepositoryLanguages(false)
	if app.languages.request != first {
		t.Fatal("automatic refresh ignored the minimum refresh interval")
	}
	app.refreshRepositoryLanguages(true)
	manual := app.languages.request
	if manual == first {
		t.Fatal("manual refresh did not bypass the refresh interval")
	}
	select {
	case <-manual.done:
	case <-time.After(5 * time.Second):
		t.Fatal("manual language refresh did not finish")
	}
	app.languages.nextRefresh = time.Now().Add(-time.Second)
	app.refreshRepositoryLanguages(false)
	if app.languages.request == manual {
		t.Fatal("automatic refresh did not resume after the interval")
	}
	select {
	case <-app.languages.request.done:
	case <-time.After(5 * time.Second):
		t.Fatal("automatic language refresh did not finish")
	}
	previous := app.languages.request
	app.library.Repositories = append(app.library.Repositories, workspace.LocalRepository{Path: t.TempDir()})
	app.refreshRepositoryLanguages(false)
	if app.languages.request == previous {
		t.Fatal("adding a repository did not bypass automatic refresh throttling")
	}
	select {
	case <-app.languages.request.done:
	case <-time.After(5 * time.Second):
		t.Fatal("updated language refresh did not finish")
	}
}

func TestNativeLanguageManualLibraryRefreshBypassesThrottle(t *testing.T) {
	app, _, finish := nativeFixture(t)
	app.screen = "library"
	path := app.library.Repositories[0].Path
	app.languages = &nativeRepositoryLanguages{
		cacheDir: t.TempDir(), entries: map[string]gitrepo.LanguageSummary{},
		paths: []string{path}, nextRefresh: time.Now().Add(time.Hour),
	}
	app.refresh()
	finish()
	request := app.languages.request
	if request == nil {
		t.Fatal("manual library refresh did not request language checking")
	}
	select {
	case <-request.done:
	case <-time.After(5 * time.Second):
		t.Fatal("manual library refresh did not finish language checking")
	}
	finish()
	if _, found := app.languages.entries[path]; !found {
		t.Fatal("manual library refresh did not publish the language result")
	}
}

func TestRepositoryLanguageCachePruning(t *testing.T) {
	cache := t.TempDir()
	kept, removed := "/repositories/kept", "/repositories/removed"
	for _, path := range []string{kept, removed} {
		if err := saveRepositoryLanguageCache(cache, path, gitrepo.LanguageSummary{Complete: true}); err != nil {
			t.Fatal(err)
		}
	}
	unmanaged := filepath.Join(cache, "notes.json")
	if err := os.WriteFile(unmanaged, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(cache, strings.Repeat("a", 64)+".json")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	pruneRepositoryLanguageCache(context.Background(), cache, []string{kept})
	if _, err := os.Stat(repositoryLanguageCachePath(cache, removed)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed repository retained its cache: %v", err)
	}
	for _, path := range []string{repositoryLanguageCachePath(cache, kept), unmanaged, directory} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("pruning removed an active cache or unrelated entry: %s: %v", path, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pruneRepositoryLanguageCache(ctx, cache, nil)
	if _, err := os.Stat(repositoryLanguageCachePath(cache, kept)); err != nil {
		t.Fatalf("cancelled pruning deleted a cache: %v", err)
	}
}

func TestNativeLanguageRemovalWaitsForWriterBeforePruning(t *testing.T) {
	app, path, _ := nativeFixture(t)
	app.screen = "library"
	app.library.Repositories = nil
	cache := t.TempDir()
	blocked := &nativeLanguageRequest{cancel: func() {}, done: make(chan struct{})}
	app.languages = &nativeRepositoryLanguages{
		cacheDir: cache, entries: map[string]gitrepo.LanguageSummary{},
		paths: []string{path}, request: blocked, nextRefresh: time.Now().Add(time.Hour),
	}
	app.refreshRepositoryLanguages(false)
	current := app.languages.request
	if current == blocked {
		close(blocked.done)
		t.Fatal("removing a repository did not bypass automatic refresh throttling")
	}
	if err := saveRepositoryLanguageCache(cache, path, gitrepo.LanguageSummary{Complete: true}); err != nil {
		close(blocked.done)
		t.Fatal(err)
	}
	close(blocked.done)
	select {
	case <-current.done:
	case <-time.After(5 * time.Second):
		t.Fatal("removal cleanup did not finish")
	}
	if _, err := os.Stat(repositoryLanguageCachePath(cache, path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous language writer recreated a removed cache: %v", err)
	}
}

func TestNativeLibraryRefreshFailureKeepsLibrary(t *testing.T) {
	app, path, finish := nativeFixture(t)
	app.screen = "library"
	app.library = app.service.Library()
	if len(app.library.Repositories) != 1 || app.library.Repositories[0].Path == "" {
		t.Fatalf("夹具仓库未加入列表: %+v %s", app.library, path)
	}
	// 取消后 RefreshLibrary 返回空列表和错误，不能覆盖现有列表或按空列表清理语言缓存。
	app.cancel()
	app.refresh()
	finish()
	if app.problem == "" || len(app.library.Repositories) != 1 {
		t.Fatalf("刷新失败后列表被替换: problem=%q library=%+v", app.problem, app.library)
	}
}
