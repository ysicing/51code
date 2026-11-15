// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func insightsCommit(t *testing.T, dir, date, name, email string) string {
	t.Helper()
	command := exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", "activity")
	command.Env = append(gitEnvironment(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date, "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, output)
	}
	return git(t, dir, "rev-parse", "HEAD")
}

func TestInsightsAggregation(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	write(t, dir, "tabs\tand\nlines.txt", "one\ntwo\n")
	write(t, dir, "binary.dat", "\x00\x01")
	git(t, dir, "add", ".")
	insightsCommit(t, dir, "2026-11-01T12:00:00+08:00", "Alias", "alias@example.test")
	git(t, dir, "mv", "tabs\tand\nlines.txt", "renamed.txt")
	insightsCommit(t, dir, "2026-11-02T12:00:00+08:00", "Canonical", "canonical@example.test")
	insightsCommit(t, dir, "2026-11-03T12:00:00+08:00", "Canonical", "canonical@example.test")
	write(t, dir, ".mailmap", "Canonical <canonical@example.test> Alias <alias@example.test>\n")
	source, err := repo.InsightsSource(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	zone := time.FixedZone("test", 8*3600)
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, zone)
	result, err := repo.Insights(ctx, source, from, from.AddDate(0, 0, 3))
	if err != nil {
		t.Fatal(err)
	}
	if result.Commits != 3 || result.Contributors != 1 || result.ActiveDays != 3 || result.ChangedFiles != 3 {
		t.Fatalf("summary: %+v", result)
	}
	if len(result.Days) != 3 || result.Days[0].Added != 2 || result.Days[1].Added != 0 || result.Days[1].Deleted != 0 {
		t.Fatalf("daily: %+v", result.Days)
	}
	if result.Authors[0].Name != "Canonical" || result.Authors[0].Commits != 3 {
		t.Fatalf("authors: %+v", result.Authors)
	}
	found := false
	for _, file := range result.Files {
		found = found || file.Path == "tabs\tand\nlines.txt"
	}
	if !found {
		t.Fatalf("literal paths lost: %+v", result.Files)
	}
	// 半开日期区间不能多算结束当天的提交。
	short, err := repo.Insights(ctx, source, from, from.AddDate(0, 0, 1))
	if err != nil || short.Commits != 1 {
		t.Fatalf("boundary: %+v %v", short, err)
	}
}

func TestInsightsBranchMergeAndEmpty(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	source, err := repo.InsightsSource(ctx, "HEAD")
	if err != nil || source.Commit != "" {
		t.Fatalf("unborn: %+v %v", source, err)
	}
	from := time.Now().AddDate(0, 0, -3)
	empty, err := repo.Insights(ctx, source, from, time.Now().AddDate(0, 0, 1))
	if err != nil || empty.Commits != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	git(t, dir, "commit", "--allow-empty", "-m", "root")
	git(t, dir, "checkout", "-b", "feature")
	git(t, dir, "commit", "--allow-empty", "-m", "feature")
	git(t, dir, "checkout", "main")
	main := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "merge", "--no-ff", "feature", "-m", "merge")
	source, err = repo.InsightsSource(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.Insights(ctx, source, from, time.Now().AddDate(0, 0, 1))
	if err != nil || result.Commits != 2 || result.ChangedFiles != 0 {
		t.Fatalf("merge: %+v %v", result, err)
	}
	source.Commit = main
	result, err = repo.Insights(ctx, source, from, time.Now().AddDate(0, 0, 1))
	if err != nil || result.Commits != 1 {
		t.Fatalf("branch: %+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.Insights(ctx, source, from, time.Now()); err == nil {
		t.Fatal("canceled analysis succeeded")
	}
}

func TestInsightsParserRejectsLimitsAndMalformedRecords(t *testing.T) {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	parser := newInsightsParser(from, from.AddDate(0, 0, 1))
	if _, err := parser.Write([]byte(strings.Repeat("x", insightsTokenLimit+1))); err == nil {
		t.Fatal("unbounded token accepted")
	}
	parser = newInsightsParser(from, from.AddDate(0, 0, 1))
	if _, err := parser.Write([]byte("invalid\000")); err == nil {
		t.Fatal("invalid numstat accepted")
	}
}

func TestInsightsNonMonotonicDates(t *testing.T) {
	repo, dir := fixture(t)
	for _, date := range []string{"2026-11-05T12:00:00Z", "2026-10-01T12:00:00Z", "2026-11-06T12:00:00Z", "2026-11-10T00:00:00Z"} {
		insightsCommit(t, dir, date, "Test", "test@example.test")
	}
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	source, err := repo.InsightsSource(context.Background(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.Insights(context.Background(), source, from, from.AddDate(0, 0, 9))
	if err != nil || result.Commits != 2 {
		t.Fatalf("nonmonotonic history/date boundary: %+v %v", result, err)
	}
	write(t, dir, ".mailmap", "Other <other@example.test> <test@example.test>\n")
	changed, err := repo.InsightsSource(context.Background(), "HEAD")
	if err != nil || changed.Mailmap == source.Mailmap {
		t.Fatal("mailmap edit did not change identity", err)
	}
	if _, err := repo.Insights(context.Background(), source, from, from.AddDate(0, 0, 9)); err == nil {
		t.Fatal("mailmap changed during scan but stale aggregate accepted")
	}
}

func TestInsightsShallowBoundaryInvalidatesSource(t *testing.T) {
	_, dir := fixture(t)
	for range 3 {
		git(t, dir, "commit", "--allow-empty", "-m", "local history")
	}
	cloned := filepath.Join(t.TempDir(), "shallow")
	remote := (&url.URL{Scheme: "file", Path: dir}).String()
	git(t, dir, "clone", "--depth=1", remote, cloned)
	repo, err := Open(context.Background(), cloned)
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.InsightsSource(context.Background(), "HEAD")
	if err != nil || before.Shallow == "" {
		t.Fatal("missing shallow identity", err)
	}
	git(t, cloned, "fetch", "--deepen=1", "origin")
	after, err := repo.InsightsSource(context.Background(), "HEAD")
	if err != nil || before.Commit != after.Commit || before.Shallow == after.Shallow {
		t.Fatalf("deepen without HEAD change: %+v %+v %v", before, after, err)
	}
	from, until := time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, 1)
	result, err := repo.Insights(context.Background(), after, from, until)
	if err != nil || result.Commits != 2 || !result.Shallow {
		t.Fatalf("shallow aggregate: %+v %v", result, err)
	}
}

func TestInsightsFallsBackWithoutSinceAsFilter(t *testing.T) {
	repo, dir := fixture(t)
	ctx := context.Background()
	// 提交时间倒置：中间提交早于起点，--since 会在此停止并漏掉更早的范围内提交。
	insightsCommit(t, dir, "2026-11-02T10:00:00+08:00", "First", "first@example.test")
	insightsCommit(t, dir, "2026-11-01T12:00:00+08:00", "Old", "old@example.test")
	insightsCommit(t, dir, "2026-11-02T12:00:00+08:00", "New", "new@example.test")
	source, err := repo.InsightsSource(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 2, 0, 0, 0, 0, time.FixedZone("test", 8*3600))
	want, err := repo.Insights(ctx, source, from, from.AddDate(0, 0, 1))
	if err != nil || want.Commits != 2 {
		t.Fatalf("filter result = %+v, %v", want, err)
	}
	// 模拟 Git 2.37 之前的版本：不认识 --since-as-filter。
	installHistoryGitWrapper(t, `for arg in "$@"; do
case "$arg" in --since-as-filter=*) echo "fatal: unrecognized argument: $arg" >&2; exit 128;; esac
done
exec "$XSOURCE_REAL_GIT" "$@"
`)
	got, err := repo.Insights(ctx, source, from, from.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback = %+v, want %+v", got, want)
	}
	// 范围内没有提交时不能把空列表交给 log --stdin，否则会退回统计 HEAD。
	empty, err := repo.Insights(ctx, source, from.AddDate(0, 0, 3), from.AddDate(0, 0, 4))
	if err != nil || empty.Commits != 0 {
		t.Fatalf("empty fallback = %+v, %v", empty, err)
	}
}

func TestInsightsDaysAcrossMidnightDST(t *testing.T) {
	zone, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Skip(err)
	}
	// 2026-09-06 00:00 跳到 01:00，当天的起点是跳变时刻。
	from := InsightsDayStart(time.Date(2026, 9, 1, 12, 0, 0, 0, zone), 0)
	if start := InsightsDayStart(from, 5); start.Format(time.RFC3339) != "2026-09-06T01:00:00-03:00" {
		t.Fatalf("gap day start = %s", start.Format(time.RFC3339))
	}
	parser := newInsightsParser(from, InsightsDayStart(from, 10))
	if len(parser.days) != 10 {
		t.Fatalf("days = %d", len(parser.days))
	}
	for index, day := range parser.days {
		if want := time.Date(2026, 9, 1+index, 0, 0, 0, 0, time.UTC).Format("2006-01-02"); day.Date != want {
			t.Fatalf("day %d = %s, want %s", index, day.Date, want)
		}
	}
}
