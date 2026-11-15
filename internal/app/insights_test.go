// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"

	"github.com/egoist/mygo/ui"
)

func TestInsightsCalendarRange(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	from, until := insightsDates(time.Date(2026, 11, 2, 12, 0, 0, 0, zone), 7)
	if from.Format("2006-01-02") != "2026-10-27" || until.Format("2006-01-02") != "2026-11-03" ||
		until.Sub(from) != 169*time.Hour {
		t.Fatalf("DST calendar range: %v %v", from, until)
	}
	// 午夜夏令时跳变（America/Santiago 2026-09-06）也只能有 90 个不重复的日期，且缓存可往返。
	santiago, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Skip(err)
	}
	from, until = insightsDates(time.Date(2026, 11, 15, 12, 0, 0, 0, santiago), 90)
	data, seen := gitrepo.RepositoryInsights{}, map[string]bool{}
	for date := from; date.Before(until); date = gitrepo.InsightsDayStart(date, 1) {
		label := date.Format("2006-01-02")
		if seen[label] {
			t.Fatalf("duplicate day %s", label)
		}
		seen[label] = true
		data.Days = append(data.Days, gitrepo.InsightsDay{Date: label})
	}
	if len(data.Days) != 90 {
		t.Fatalf("days = %d", len(data.Days))
	}
	path := filepath.Join(t.TempDir(), "insights.json")
	writeInsightsCache(path, "key", data)
	if readInsightsCache(path, "key", from, until) == nil {
		t.Fatal("DST range cache rejected")
	}
}

func TestInsightsCacheValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "insights.json")
	from, until := insightsDates(time.Now(), 7)
	key := insightsCacheKey("/repo", "HEAD", gitrepo.InsightsSourceInfo{Commit: "abc"}, from, until)
	data := gitrepo.RepositoryInsights{}
	for date := from; date.Before(until); date = gitrepo.InsightsDayStart(date, 1) {
		data.Days = append(data.Days, gitrepo.InsightsDay{Date: date.Format("2006-01-02")})
	}
	writeInsightsCache(path, key, data)
	if cached := readInsightsCache(path, key, from, until); cached == nil {
		t.Fatal("cache round trip failed")
	}
	if cached := readInsightsCache(path, key+"changed", from, until); cached != nil {
		t.Fatal("cache accepted another commit")
	}
	if err := os.WriteFile(path, []byte(`{"Key":`), 0600); err != nil {
		t.Fatal(err)
	}
	if cached := readInsightsCache(path, key, from, until); cached != nil {
		t.Fatal("corrupt cache accepted")
	}
}

func TestNativeInsightsLifecycle(t *testing.T) {
	app, path, finish := nativeFixture(t)
	if !app.prefs.SidebarItems.Insights {
		t.Fatal("insights must default on")
	}
	app.switchWorkspace("insights")
	finish()
	if app.insights == nil || app.insights.result == nil || app.insights.result.Commits != 1 {
		t.Fatalf("initial insights: %+v %s", app.insights, app.problem)
	}
	app.refreshInsights(false)
	if app.readTask != nil {
		t.Fatal("focus refresh was not throttled")
	}
	old := app.insights.result
	nativeGit(t, path, "commit", "--allow-empty", "-m", "empty")
	app.refreshInsights(true)
	finish()
	if app.insights.result.Commits != 2 || app.insights.result == old {
		t.Fatal("empty commit did not invalidate cache")
	}
	app.refreshInsights(true)
	app.switchWorkspace("changes")
	finish()
	if app.readTask != nil || app.screen != "changes" {
		t.Fatal("navigation did not cancel analysis")
	}
	app.switchWorkspace("insights")
	// 近期结果可以直接复用，但禁用功能必须离开页面并取消读取。
	tester := sidebarSettingsTester(t, app)
	if err := tester.Click("洞察"); err != nil {
		t.Fatal(err)
	}
	if app.screen != "changes" || app.readTask != nil {
		t.Fatal("disabled insights remained active")
	}
}

func TestNativeInsightsLayout(t *testing.T) {
	app, _, finish := nativeFixture(t)
	app.prefs.SidebarItems.Insights = true
	app.switchWorkspace("insights")
	finish()
	for _, size := range [][2]int{{1024, 740}, {1320, 900}} {
		for _, appearance := range []string{appearanceLight, appearanceDark} {
			app.prefs.Appearance = appearance
			tester := ui.NewTester(app.view, size[0], size[1])
			for _, label := range []string{"提交活跃度", "文本变更趋势", "贡献者", "文件变更热点"} {
				if _, found := tester.Find(label); !found {
					t.Errorf("missing chart: %s", label)
				}
			}
			for _, label := range []string{"不含合并提交"} {
				if _, found := tester.Find(label); found {
					t.Errorf("unexpected control: %s", label)
				}
			}
			if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
				saveNativeScreenshot(t, tester, directory, fmt.Sprintf("insights-%s-%d.png", appearance, size[0]))
			}
		}
	}
}

func TestNativeInsightsFiltersAndFailure(t *testing.T) {
	app, path, finish := nativeFixture(t)
	app.prefs.SidebarItems.Insights = true
	app.switchWorkspace("insights")
	finish()
	tester := ui.NewTester(app.view, 1024, 740)
	if err := tester.Click("7 天"); err != nil {
		t.Fatal(err)
	}
	finish()
	if app.insights.period != "7" || len(app.insights.result.Days) != 7 {
		t.Fatal("period selection did not refresh")
	}
	nativeGit(t, path, "checkout", "-b", "new-branch")
	nativeGit(t, path, "commit", "--allow-empty", "-m", "new branch")
	// 聚焦先刷新仓库快照，引用变化后无需等待节流即重读洞察。
	app.refreshOnFocus()
	finish()
	finish()
	tester.Frame()
	if app.insights.result.Commits != 2 || app.repository.snapshot.Branch != "new-branch" {
		t.Fatal("external branch change not detected")
	}
	if _, found := tester.Find("当前分支 · new-branch"); !found {
		t.Error("branch filter label not refreshed")
	}
	app.insights.ref, app.insights.result, app.insights.checked = "refs/heads/main", nil, time.Time{}
	app.refreshInsights(false)
	finish()
	if app.insights.result.Commits != 1 || nativeGit(t, path, "branch", "--show-current") != "new-branch" {
		t.Fatal("branch filter changed checkout or reached wrong commits")
	}
	result := app.insights.result
	nativeGit(t, path, "branch", "-D", "main")
	app.refreshInsights(true)
	finish()
	if app.insights.problem == "" || app.insights.result != result {
		t.Fatal("refresh failure discarded valid result")
	}
	// 第一次聚焦发现 main 已删除，引用变化会立即重读；之后的失败检查仍受节流。
	app.refreshOnFocus()
	finish()
	if app.readTask == nil {
		t.Fatal("reference change did not bypass throttle")
	}
	finish()
	app.refreshOnFocus()
	finish()
	if app.readTask != nil || app.insights.problem == "" {
		t.Fatal("failed checks should also be throttled")
	}
}

func TestNativeInsightsWaitsForBusyTask(t *testing.T) {
	app, path, finish := nativeFixture(t)
	app.prefs.SidebarItems.Insights = true
	app.busy = true
	app.switchWorkspace("insights")
	if app.insights == nil || app.insights.from.IsZero() || app.readTask != nil {
		t.Fatalf("busy entry state: %+v", app.insights)
	}
	tester := ui.NewTester(app.view, 1024, 740)
	for _, label := range []string{"统计范围", "分支", "获取", "拉取", "推送"} {
		if _, found := tester.Find(label); !found {
			t.Errorf("missing control while busy: %s", label)
		}
	}
	app.busy = false
	nativeGit(t, path, "commit", "--allow-empty", "-m", "second")
	// ⌘R 走常规仓库刷新，结束后由 runTask 补读洞察。
	app.refresh()
	finish()
	if app.readTask == nil || app.readTask.kind != nativeReadInsights {
		t.Fatal("insights not reloaded after busy task")
	}
	finish()
	if app.insights.result == nil || app.insights.result.Commits != 2 || app.repository.snapshot.Head == "" {
		t.Fatalf("insights after refresh: %+v %s", app.insights.result, app.problem)
	}
}

func TestPruneInsightsCache(t *testing.T) {
	directory := t.TempDir()
	name := func(path string) string {
		return filepath.Join(directory, fmt.Sprintf("%x.json", sha256.Sum256([]byte(path))))
	}
	current, known, stale := name("/current"), name("/known"), name("/removed")
	other := filepath.Join(directory, "notes.json")
	for _, file := range []string{current, known, stale, other} {
		if err := os.WriteFile(file, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pruneInsightsCache(current, []string{"/known"})
	for file, want := range map[string]bool{current: true, known: true, stale: false, other: true} {
		if _, err := os.Stat(file); (err == nil) != want {
			t.Errorf("%s exists=%v want %v", filepath.Base(file), err == nil, want)
		}
	}
}

func TestNativeInsightsReplacesInFlightBranch(t *testing.T) {
	app, path, finish := nativeFixture(t)
	nativeGit(t, path, "checkout", "-b", "other")
	nativeGit(t, path, "commit", "--allow-empty", "-m", "other")
	nativeGit(t, path, "checkout", "main")
	block := make(chan struct{})
	app.readDone = block
	app.prefs.SidebarItems.Insights = true
	app.switchWorkspace("insights")
	first := app.readTask
	app.insights.ref = "refs/heads/other"
	app.refreshInsights(false)
	replaced := app.readTask != first
	close(block)
	finish()
	if replaced {
		finish()
	}
	if !replaced || app.insights.result == nil || app.insights.result.Commits != 2 {
		t.Fatal("new branch did not replace in-flight analysis")
	}
}

func TestInsightsCacheRejectsInvalidDates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "insights.json")
	data := gitrepo.RepositoryInsights{Commits: 1, Days: []gitrepo.InsightsDay{{Date: ""}}}
	writeInsightsCache(path, "key", data)
	from, until := insightsDates(time.Now(), 7)
	if readInsightsCache(path, "key", from, until) != nil {
		t.Fatal("cache with invalid dates accepted")
	}
}

func TestNativeInsightsEmptyAndLongPaths(t *testing.T) {
	app, _, finish := nativeFixture(t)
	app.prefs.SidebarItems.Insights = true
	app.switchWorkspace("insights")
	finish()
	app.insights.result = &gitrepo.RepositoryInsights{}
	tester := ui.NewTester(app.view, 900, 640)
	if _, found := tester.Find("此范围内暂无提交"); !found {
		t.Fatal("missing empty state")
	}
	app.insights.result = &gitrepo.RepositoryInsights{
		Commits: 1, Contributors: 1, ChangedFiles: 1, ActiveDays: 1,
		Days:    []gitrepo.InsightsDay{{Date: "2026-11-15", Commits: 1, Added: 1}},
		Authors: []gitrepo.InsightsAuthor{{Name: strings.Repeat("长作者", 40), Commits: 1}},
		Files:   []gitrepo.InsightsFile{{Path: strings.Repeat("long-directory/", 40) + "文件.go", Commits: 1}},
	}
	tester.Frame()
	for _, label := range []string{"统计范围", "分支", "刷新"} {
		info, found := tester.Find(label)
		if !found || info.X < 0 || info.X+info.W > 900 {
			t.Fatalf("compact filter clipped: %s %+v", label, info)
		}
	}
	if directory := os.Getenv("XSOURCETREE_TEST_SCREENSHOTS"); directory != "" {
		saveNativeScreenshot(t, tester, directory, "insights-compact-long-paths.png")
	}
}
