// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestScanStagedSecretsUsesIndexAndReportsLocations(t *testing.T) {
	repo, path := fixture(t)
	ctx := context.Background()
	filename := "配置 file.env"
	synthetic := "ghp_" + "R7c4Wk2nH3xV8eJ9qP0uL5rD1mS6aT4yN8zB"
	write(t, path, filename, "first line\nsecond line\n")
	git(t, path, "add", "--", filename)
	git(t, path, "commit", "-m", "base")
	write(t, path, filename, "first line\nsecond line\nTOKEN="+synthetic+"\n")
	before, err := repo.ScanStagedSecrets(ctx)
	if err != nil || len(before.Findings) != 0 {
		t.Fatalf("unstaged content was scanned: %+v, %v", before, err)
	}
	git(t, path, "add", "--", filename)
	write(t, path, filename, "first line\nsecond line\nclean working tree\n")
	scan, err := repo.ScanStagedSecrets(ctx)
	if err != nil || len(scan.Findings) == 0 || scan.Token == "" {
		t.Fatalf("scan did not detect staged content: %+v, %v", scan, err)
	}
	for _, finding := range scan.Findings {
		if finding.Path != filename || finding.Line != 3 {
			t.Fatalf("wrong finding location: %+v", finding)
		}
	}
	serialized, err := json.Marshal(scan)
	if err != nil || strings.Contains(string(serialized), synthetic) {
		t.Fatal("scan result contains the matched value")
	}
	git(t, path, "add", "--", filename)
	if err := repo.CommitInspected(ctx, "stale scan", scan.Token); err == nil {
		t.Fatal("the old scan authorized an updated index")
	}
}

func TestScanStagedSecretsSupportsFirstCommitAndRejectsOversize(t *testing.T) {
	repo, path := fixture(t)
	ctx := context.Background()
	write(t, path, "initial.env", "TOKEN="+"ghp_"+"R7c4Wk2nH3xV8eJ9qP0uL5rD1mS6aT4yN8zB")
	git(t, path, "add", "--", "initial.env")
	scan, err := repo.ScanStagedSecrets(ctx)
	if err != nil || len(scan.Findings) == 0 || scan.Findings[0].Line != 1 {
		t.Fatalf("initial commit scan: %+v, %v", scan, err)
	}
	write(t, path, "big.txt", strings.Repeat("test line\n", outputLimit/8))
	git(t, path, "add", "--", "big.txt")
	scan, err = repo.ScanStagedSecrets(ctx)
	if err == nil || scan.Token != "" {
		t.Fatalf("oversized scan returned a usable token: %+v, %v", scan, err)
	}
}

func TestScanStagedSecretsSkipsLargeBinary(t *testing.T) {
	repo, path := fixture(t)
	write(t, path, "image.bin", "\x00"+strings.Repeat("x", 5<<20))
	write(t, path, "token.env", "TOKEN="+"ghp_"+"R7c4Wk2nH3xV8eJ9qP0uL5rD1mS6aT4yN8zB"+"\n")
	git(t, path, "add", "--", "image.bin", "token.env")

	scan, err := repo.ScanStagedSecrets(context.Background())
	if err != nil || scan.Token == "" || len(scan.Findings) == 0 {
		t.Fatalf("scan with staged binary: %+v, %v", scan, err)
	}
	for _, finding := range scan.Findings {
		if finding.Path != "token.env" || finding.Line != 1 {
			t.Fatalf("wrong finding location: %+v", finding)
		}
	}
}
