// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/ysicing/51code/internal/gitrepo"

	"github.com/junegunn/fzf/src/algo"
	"github.com/junegunn/fzf/src/util"
)

var initializeSearchRanking = sync.OnceFunc(func() { algo.Init("default") })

type nativePathScore struct {
	path  string
	score int
}

func rankQuickPaths(ctx context.Context, paths []string, query string) []nativePathScore {
	initializeSearchRanking()
	tokens := strings.Fields(strings.ToLower(query))
	slab := util.MakeSlab(32*1024, 2048)
	patterns := make([][]rune, len(tokens))
	for index, token := range tokens {
		patterns[index] = []rune(token)
	}
	var ranked []nativePathScore
	for _, path := range paths {
		if ctx.Err() != nil {
			return nil
		}
		text := util.ToChars([]byte(path))
		score := 0
		for _, pattern := range patterns {
			result, _ := algo.FuzzyMatchV2(false, false, true, &text, pattern, false, slab)
			if result.Start < 0 {
				score = -1
				break
			}
			score += result.Score
		}
		if score >= 0 {
			ranked = append(ranked, nativePathScore{path: path, score: score})
		}
	}
	slices.SortFunc(ranked, func(first, second nativePathScore) int {
		return cmp.Or(cmp.Compare(second.score, first.score), cmp.Compare(first.path, second.path))
	})
	return ranked
}

func rankWorktreeMatches(matches []gitrepo.WorktreeSearchMatch, query string) []gitrepo.WorktreeSearchMatch {
	initializeSearchRanking()
	pattern := []rune(strings.ToLower(query))
	type hit struct {
		gitrepo.WorktreeSearchMatch
		score int
	}
	ranked := make([]hit, 0, len(matches))
	best := make(map[string]int)
	for _, match := range matches {
		line := util.ToChars([]byte(match.Text))
		result, _ := algo.ExactMatchNaive(false, false, true, &line, pattern, false, nil)
		ranked = append(ranked, hit{WorktreeSearchMatch: match, score: result.Score})
		best[match.Path] = max(best[match.Path], result.Score)
	}
	slices.SortFunc(ranked, func(first, second hit) int {
		return cmp.Or(cmp.Compare(best[second.Path], best[first.Path]), cmp.Compare(first.Path, second.Path),
			cmp.Compare(second.score, first.score), cmp.Compare(first.Line, second.Line))
	})
	result := make([]gitrepo.WorktreeSearchMatch, len(ranked))
	for index, match := range ranked {
		result[index] = match.WorktreeSearchMatch
	}
	return result
}
