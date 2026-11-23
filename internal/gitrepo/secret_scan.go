// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gitleaks/go-gitdiff/gitdiff"
	"github.com/rs/zerolog"
	"github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/logging"
)

const secretFindingLimit = 100

type SecretFinding struct {
	Path   string
	Line   int
	RuleID string
}

type SecretScan struct {
	Token    string
	Findings []SecretFinding
	Limited  bool
}

var secretScanConfig = sync.OnceValues(func() (config.Config, error) {
	// SDK 的调试日志可能包含原始匹配值。仅关闭 SDK 自己的日志。
	logging.Logger = zerolog.Nop()
	detector, err := detect.NewDetectorDefaultConfig()
	if err != nil {
		return config.Config{}, errors.New("无法加载 Gitleaks 内置规则")
	}
	return detector.Config, nil
})

func (repo *Repository) ScanStagedSecrets(ctx context.Context) (SecretScan, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := repo.lockHistory(ctx); err != nil {
		return SecretScan{}, err
	}
	defer repo.mu.Unlock()

	token, patch, err := repo.stagedSecretPatch(ctx)
	if err != nil {
		return SecretScan{}, err
	}
	scan, err := scanSecretPatch(ctx, patch)
	if err != nil {
		return SecretScan{}, err
	}
	current, err := repo.commitToken(ctx)
	if err != nil {
		return SecretScan{}, err
	}
	if current != token {
		return SecretScan{}, errors.New("暂存区或当前提交已变化，请重新扫描")
	}
	scan.Token = token
	return scan, nil
}

func (repo *Repository) stagedSecretPatch(ctx context.Context) (string, string, error) {
	token, err := repo.commitToken(ctx)
	if err != nil {
		return "", "", err
	}
	head, tree, _ := strings.Cut(token, "\n")
	if head == "" {
		head, err = repo.git(ctx, "hash-object", "-t", "tree", "--stdin")
		if err != nil {
			return "", "", err
		}
		head = strings.TrimSpace(head)
	}
	patch, limited, err := runCapped(ctx, repo.Path, "", 30*time.Second, outputLimit,
		"-c", "core.quotePath=true", "-c", "diff.suppressBlankEmpty=false",
		"diff", "--patch", "--text", "--no-ext-diff", "--no-textconv", "--no-renames",
		"--no-color", "--no-relative", "--no-function-context", "--word-diff=none",
		"--submodule=short", "--ignore-submodules=none", "--diff-filter=ACMT",
		"--src-prefix=a/", "--dst-prefix=b/", "--output-indicator-new=+",
		"--output-indicator-old=-", "--output-indicator-context= ", "--unified=0",
		"--inter-hunk-context=0", head, tree, "--")
	if err != nil {
		return "", "", errors.New("无法读取用于密钥扫描的暂存差异")
	}
	if limited {
		return "", "", errors.New("暂存差异超过 4 MiB，请拆分提交后重新扫描")
	}
	return token, patch, nil
}

func scanSecretPatch(ctx context.Context, patch string) (SecretScan, error) {
	cfg, err := secretScanConfig()
	if err != nil {
		return SecretScan{}, err
	}
	files, err := gitdiff.Parse(strings.NewReader(patch))
	if err != nil {
		return SecretScan{}, errors.New("无法解析暂存差异")
	}
	detector := detect.NewDetectorContext(ctx, cfg)
	var scan SecretScan
	seen := make(map[SecretFinding]bool)
	parsed := 0
	for file := range files {
		parsed++
		// 即使超时或结果已满，也要读完解析通道，避免阻塞 SDK 的解析协程。
		if ctx.Err() != nil || scan.Limited {
			continue
		}
		for _, fragment := range file.TextFragments {
			if ctx.Err() != nil || scan.Limited {
				break
			}
			for _, finding := range detector.DetectContext(ctx, detect.Fragment{
				Raw: fragment.Raw(gitdiff.OpAdd), FilePath: file.NewName,
				StartLine: int(fragment.NewPosition),
			}) {
				result := SecretFinding{Path: finding.File, Line: finding.StartLine, RuleID: finding.RuleID}
				if seen[result] {
					continue
				}
				if len(scan.Findings) == secretFindingLimit {
					scan.Limited = true
					break
				}
				seen[result] = true
				scan.Findings = append(scan.Findings, result)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return SecretScan{}, err
	}
	// gitdiff 的流式接口会在解析异常时结束通道，不能把缺失的文件当成无命中。
	expected := strings.Count("\n"+patch, "\ndiff --git ")
	if parsed != expected {
		return SecretScan{}, errors.New("暂存差异解析不完整，请重新扫描")
	}
	slices.SortFunc(scan.Findings, func(left, right SecretFinding) int {
		if order := strings.Compare(left.Path, right.Path); order != 0 {
			return order
		}
		if left.Line != right.Line {
			return left.Line - right.Line
		}
		return strings.Compare(left.RuleID, right.RuleID)
	})
	return scan, nil
}
