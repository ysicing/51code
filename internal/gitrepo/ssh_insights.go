// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

func (repo SSHWorkspace) Insights(ctx context.Context, ref string, from, until time.Time) (RepositoryInsights, error) {
	if strings.ContainsAny(ref, "\x00\r\n") || ref != "HEAD" && !strings.HasPrefix(ref, "refs/heads/") {
		return RepositoryInsights{}, errors.New("无效的分支引用")
	}
	raw, err := repo.read(ctx, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return RepositoryInsights{}, err
	}
	var source InsightsSourceInfo
	if strings.TrimSpace(raw) == "true" {
		source.Shallow = "true"
	}
	head, err := repo.read(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		if ref != "HEAD" || !sshExitCode(err, 1) {
			return RepositoryInsights{}, err
		}
		symbolic, symbolicErr := repo.read(ctx, "symbolic-ref", "-q", "HEAD")
		if symbolicErr != nil {
			return RepositoryInsights{}, err
		}
		_, verifyErr := repo.read(ctx, "show-ref", "--verify", "--quiet", strings.TrimSpace(symbolic))
		if !sshExitCode(verifyErr, 1) {
			return RepositoryInsights{}, err
		}
	} else {
		source.Commit = strings.TrimSpace(head)
		if err := validCommitHash(source.Commit); err != nil {
			return RepositoryInsights{}, err
		}
	}
	return calculateInsights(ctx, source, from, until, func(ctx context.Context, stdin string, stdout io.Writer, args ...string) error {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return repo.gitTo(ctx, stdin, stdout, args...)
	})
}
