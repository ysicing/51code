// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"testing"
	"time"
)

func TestNativeCommitGraphAttemptsEachRepositoryOnce(t *testing.T) {
	app, path, _ := nativeFixture(t)
	calls := make(chan string, 4)
	app.commitGraphs = &nativeCommitGraphs{
		attempted: map[string]bool{}, slot: make(chan struct{}, 1),
		ensure: func(_ context.Context, path string) (bool, error) {
			calls <- path
			return true, nil
		},
	}
	app.maintainCommitGraph(path)
	app.maintainCommitGraph(path)
	app.maintainCommitGraph("")
	select {
	case got := <-calls:
		if got != path {
			t.Fatalf("path = %q, want %q", got, path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("commit-graph 后台任务未执行")
	}
	select {
	case got := <-calls:
		t.Fatalf("unexpected second attempt for %q", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestNativeCommitGraphStopsWhenResourcesUnavailable(t *testing.T) {
	app, path, _ := nativeFixture(t)
	called := make(chan struct{}, 1)
	waited := make(chan struct{})
	app.commitGraphs = &nativeCommitGraphs{
		attempted: map[string]bool{}, slot: make(chan struct{}, 1),
		waitForResources: func(context.Context) error {
			close(waited)
			return context.DeadlineExceeded
		},
		ensure: func(context.Context, string) (bool, error) {
			called <- struct{}{}
			return true, nil
		},
	}
	app.maintainCommitGraph(path)
	<-waited
	select {
	case <-called:
		t.Fatal("资源不足时不应写入 commit-graph")
	case <-time.After(200 * time.Millisecond):
	}
}
