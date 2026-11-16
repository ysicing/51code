// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"testing"
	"time"
)

func TestHistoryPreservesAuthorAndCommitterDates(t *testing.T) {
	repo, path := fixture(t)
	t.Setenv("GIT_AUTHOR_DATE", "2026-10-01T10:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-11-10T12:00:00Z")
	git(t, path, "commit", "--allow-empty", "-m", "two dates")
	for _, search := range []bool{false, true} {
		var commits []Commit
		var err error
		if search {
			commits, err = repo.SearchHistory(context.Background(), "", "", 0, HistoryFilter{})
		} else {
			commits, err = repo.History(context.Background(), 0)
		}
		if err != nil || len(commits) != 1 {
			t.Fatalf("读取历史失败: %v %v", commits, err)
		}
		for _, date := range []struct{ value, want string }{
			{commits[0].Date, "2026-10-01T10:00:00Z"},
			{commits[0].CommitterDate, "2026-11-10T12:00:00Z"},
		} {
			actual, err := time.Parse(time.RFC3339, date.value)
			expected, parseErr := time.Parse(time.RFC3339, date.want)
			if err != nil || parseErr != nil || !actual.Equal(expected) {
				t.Fatalf("作者日期或提交者日期丢失: %+v", commits[0])
			}
		}
	}
	filtered, err := repo.SearchHistory(context.Background(), "", "", 0, HistoryFilter{Since: "2026-11-01"})
	if err != nil || len(filtered) != 0 {
		t.Fatalf("日期显示不应改变作者日期筛选: %v %v", filtered, err)
	}
}
