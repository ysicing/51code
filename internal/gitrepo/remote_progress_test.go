// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRemoteProgressWriterKeepsErrors(t *testing.T) {
	var events []RemoteProgress
	var details strings.Builder
	writer := remoteProgressWriter{details: &details, report: func(progress RemoteProgress) {
		events = append(events, progress)
	}}
	for _, chunk := range []string{
		"remote: Enumerating objects: 25, done.\r\nWriting obj",
		"ects:  68% (17/25)\rWriting objects: 100% (25/25), done.\r\n",
		"remote: Resolving deltas:  50% (1/2)\r\nfatal: failed to update ref",
	} {
		_, _ = writer.Write([]byte(chunk))
	}
	writer.flush()
	want := []RemoteProgress{
		{Phase: RemotePreparing, Percent: RemotePercentUnknown},
		{Phase: RemoteSending, Percent: 68},
		{Phase: RemoteConfirming, Percent: RemotePercentUnknown},
		{Phase: RemoteResolving, Percent: 50},
	}
	if !slices.Equal(events, want) || details.String() != "fatal: failed to update ref\n" {
		t.Fatalf("events = %+v, details = %q", events, details.String())
	}
}

func TestRemoteProgressReportsGitPushAndPullUpdate(t *testing.T) {
	repo, path := fixture(t)
	remote := t.TempDir()
	git(t, remote, "init", "--bare", "-b", "main")
	git(t, path, "remote", "add", "origin", remote)
	git(t, path, "commit", "--allow-empty", "-m", "base")
	var phases []RemotePhase
	ctx := WithRemoteProgress(context.Background(), func(progress RemoteProgress) {
		phases = append(phases, progress.Phase)
	})
	configs, err := repo.RemoteConfigs(ctx)
	if err != nil || len(configs) != 1 {
		t.Fatalf("remote config = %+v, %v", configs, err)
	}
	request := PublishBranchRequest{
		Remote: "origin", Token: configs[0].Token, Name: "main",
		Branch: "main", Head: git(t, path, "rev-parse", "HEAD"),
	}
	if err := repo.PublishBranch(ctx, request); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(phases, RemoteConfirming) {
		t.Fatalf("Git did not report upload completion: %v", phases)
	}
	readerPath := filepath.Join(t.TempDir(), "reader")
	git(t, path, "clone", "--no-local", remote, readerPath)
	git(t, readerPath, "config", "fetch.unpackLimit", "1")
	reader, err := Open(ctx, readerPath)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, "change.txt", "new content\n")
	git(t, path, "add", "change.txt")
	git(t, path, "commit", "-m", "new content")
	phases = nil
	if err := repo.Remote(ctx, "push"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(phases, RemoteConfirming) {
		t.Fatalf("direct push did not report upload completion: %v", phases)
	}
	phases = nil
	if err := reader.Remote(ctx, "pull"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(phases, RemoteReceiving) || phases[len(phases)-1] != RemoteUpdating {
		t.Fatalf("pull did not report receiving before its local update: %v", phases)
	}
	if got, want := git(t, readerPath, "rev-parse", "HEAD"), git(t, path, "rev-parse", "HEAD"); got != want {
		t.Fatalf("pull left HEAD at %s, want %s", got, want)
	}
}

func TestRemoteProgressDoesNotTreatServerOutputAsLocalPushCompletion(t *testing.T) {
	progress, ok := parseRemoteProgress("remote: Writing objects: 100% (25/25), done.")
	if !ok || progress.Phase != RemoteSending || progress.Percent != 100 {
		t.Fatalf("server progress was interpreted as local push completion: %+v", progress)
	}
}
