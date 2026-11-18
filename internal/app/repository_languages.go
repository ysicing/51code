// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/internal/workspace"

	"github.com/egoist/mygo/ui"
)

type nativeRepositoryLanguages struct {
	cacheDir         string
	entries          map[string]gitrepo.LanguageSummary
	request          *nativeLanguageRequest
	waitForResources func(context.Context) error
	paths            []string
	nextRefresh      time.Time
}

type nativeLanguageRequest struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type repositoryLanguageCache struct {
	Version string                  `json:"version"`
	Summary gitrepo.LanguageSummary `json:"summary"`
	// Commit 是统计时 HEAD 指向的提交；HEAD 未变时直接复用，无需启动 Git 进程。
	// 旧缓存没有该字段，首次读取后补齐。
	Commit string `json:"commit,omitempty"`
}

func (app *nativeApp) startRepositoryLanguages(cacheDir string) {
	app.languages = &nativeRepositoryLanguages{
		cacheDir: cacheDir, entries: map[string]gitrepo.LanguageSummary{},
		waitForResources: languageResourceWaiter(),
	}
	app.refreshRepositoryLanguages(false)
}

func (app *nativeApp) refreshRepositoryLanguages(force bool) {
	state := app.languages
	if state == nil || app.closed || app.post == nil || app.screen != "library" {
		return
	}
	entries := append([]workspace.LocalRepository(nil), app.library.Repositories...)
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	changed := !slices.Equal(state.paths, paths)
	previous := state.request
	if !force && !changed {
		if previous != nil {
			select {
			case <-previous.done:
			default:
				return
			}
		}
		if time.Now().Before(state.nextRefresh) {
			return
		}
	}
	ctx, cancel := context.WithCancel(app.ctx)
	request := &nativeLanguageRequest{cancel: cancel, done: make(chan struct{})}
	if previous != nil {
		previous.cancel()
	}
	state.request = request
	state.paths, state.nextRefresh = paths, time.Now().Add(30*time.Second)
	for path := range state.entries {
		if !slices.ContainsFunc(entries, func(entry workspace.LocalRepository) bool { return entry.Path == path }) {
			delete(state.entries, path)
		}
	}
	post := app.post
	go func() {
		defer close(request.done)
		defer cancel()
		// 先等被取消的读取退出，避免连续刷新叠加 Git 进程。
		if previous != nil {
			<-previous.done
		}
		// 等旧写入者退出后再清理，防止移除的仓库缓存被取消中的任务重新创建。
		pruneRepositoryLanguageCache(ctx, state.cacheDir, paths)
		publish := func(path string, summary gitrepo.LanguageSummary, fallback bool) {
			post(func() {
				if app.closed || state.request != request ||
					!slices.ContainsFunc(app.library.Repositories, func(current workspace.LocalRepository) bool { return current.Path == path }) {
					return
				}
				// 磁盘缓存只补首屏空缺，不能覆盖本次会话中已成功读取的结果。
				if _, exists := state.entries[path]; fallback && exists {
					return
				}
				state.entries[path] = summary
			})
		}
		// 先补齐全部仓库的缓存；系统忙时不能让首个仓库的等待挡住后续标识。
		for _, entry := range entries {
			if ctx.Err() != nil {
				return
			}
			if summary, found := loadRepositoryLanguageCache(state.cacheDir, entry.Path); found {
				publish(entry.Path, summary, true)
			}
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				return
			}
			// 手动刷新交给 Git 校验实际的树；replace 引用可能让树在 HEAD ID 不变时改变。
			if !force {
				if summary, ok := unchangedRepositoryLanguages(entry.Path, state.cacheDir); ok {
					publish(entry.Path, summary, false)
					continue
				}
			}
			if state.waitForResources != nil {
				if err := state.waitForResources(ctx); err != nil {
					return
				}
			}
			// 每仓库独立超时；失败保留缓存，不占用前台 busy，也不覆盖操作错误。
			readCtx, stop := context.WithTimeout(ctx, 20*time.Second)
			summary, err := readRepositoryLanguages(readCtx, entry.Path, state.cacheDir)
			if err == nil && ctx.Err() == nil {
				publish(entry.Path, summary, false)
			}
			stop()
		}
	}()
}

func readRepositoryLanguages(ctx context.Context, path, cacheDir string) (gitrepo.LanguageSummary, error) {
	entry, found := loadRepositoryLanguageEntry(cacheDir, path)
	stored := entry.Summary
	// 在 Git 读取前后各解析一次 HEAD，两次一致才把提交记入缓存，避免与读到的 tree 错配。
	commit, _ := readHeadCommit(path)
	repo, err := gitrepo.Open(ctx, path)
	if err != nil {
		return gitrepo.LanguageSummary{}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		tree, err := repo.LanguageTree(ctx)
		if err != nil {
			return gitrepo.LanguageSummary{}, err
		}
		if found && stored.Tree == tree {
			// tree 相同但提交变了（如空提交）时只更新缓存里的提交，下次即可免进程命中。
			if commit != "" && commit != entry.Commit {
				if current, ok := readHeadCommit(path); ok && current == commit {
					_ = saveRepositoryLanguageCache(cacheDir, path, stored, commit)
				}
			}
			return stored, nil
		}
		summary := gitrepo.LanguageSummary{Complete: true}
		if tree != "" {
			summary, err = repo.Languages(ctx, tree)
			if err != nil {
				return summary, err
			}
		}
		current, err := repo.LanguageTree(ctx)
		if err != nil {
			return summary, err
		}
		if current != tree {
			continue
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if current, ok := readHeadCommit(path); !ok || current != commit {
			commit = ""
		}
		_ = saveRepositoryLanguageCache(cacheDir, path, summary, commit)
		return summary, nil
	}
	return gitrepo.LanguageSummary{}, errors.New("语言分析期间仓库内容已变化")
}

func repositoryLanguageCachePath(cacheDir, path string) string {
	if cacheDir == "" {
		return ""
	}
	key := sha256.Sum256([]byte(path))
	return filepath.Join(cacheDir, hex.EncodeToString(key[:])+".json")
}

func pruneRepositoryLanguageCache(ctx context.Context, cacheDir string, paths []string) {
	if cacheDir == "" || ctx.Err() != nil {
		return
	}
	files, err := os.ReadDir(cacheDir)
	if err != nil {
		return
	}
	keep := make(map[string]bool, len(paths))
	for _, path := range paths {
		keep[filepath.Base(repositoryLanguageCachePath(cacheDir, path))] = true
	}
	for _, file := range files {
		if ctx.Err() != nil {
			return
		}
		name := file.Name()
		if file.IsDir() || keep[name] || len(name) != 69 || !strings.HasSuffix(name, ".json") {
			continue
		}
		if _, err := hex.DecodeString(strings.TrimSuffix(name, ".json")); err == nil {
			_ = os.Remove(filepath.Join(cacheDir, name))
		}
	}
}

func loadRepositoryLanguageCache(cacheDir, path string) (gitrepo.LanguageSummary, bool) {
	entry, found := loadRepositoryLanguageEntry(cacheDir, path)
	return entry.Summary, found
}

func loadRepositoryLanguageEntry(cacheDir, path string) (repositoryLanguageCache, bool) {
	file, err := os.Open(repositoryLanguageCachePath(cacheDir, path))
	if err != nil {
		return repositoryLanguageCache{}, false
	}
	defer file.Close()
	const limit = 128 << 10
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	var stored repositoryLanguageCache
	if err != nil || len(data) > limit || json.Unmarshal(data, &stored) != nil ||
		stored.Version != gitrepo.LanguageAnalysisVersion || !validRepositoryLanguageSummary(stored.Summary) {
		return repositoryLanguageCache{}, false
	}
	if !validObjectID(stored.Commit) {
		stored.Commit = ""
	}
	return stored, true
}

// unchangedRepositoryLanguages 在缓存记录的提交仍是当前 HEAD 时返回缓存，全程不启动 Git 进程。
func unchangedRepositoryLanguages(path, cacheDir string) (gitrepo.LanguageSummary, bool) {
	entry, found := loadRepositoryLanguageEntry(cacheDir, path)
	if !found || entry.Commit == "" {
		return gitrepo.LanguageSummary{}, false
	}
	commit, ok := readHeadCommit(path)
	if !ok || commit != entry.Commit {
		return gitrepo.LanguageSummary{}, false
	}
	return entry.Summary, true
}

func validRepositoryLanguageSummary(summary gitrepo.LanguageSummary) bool {
	if summary.Tree == "" {
		return summary.Complete && len(summary.Languages) == 0
	}
	if len(summary.Tree) != 40 && len(summary.Tree) != 64 {
		return false
	}
	if _, err := hex.DecodeString(summary.Tree); err != nil {
		return false
	}
	var total int64
	for _, language := range summary.Languages {
		if language.Name == "" || language.Bytes <= 0 || language.Bytes > 32<<20 ||
			len(language.Color) != 7 || language.Color[0] != '#' {
			return false
		}
		if _, err := hex.DecodeString(language.Color[1:]); err != nil {
			return false
		}
		total += language.Bytes
		if total > 32<<20 {
			return false
		}
	}
	return true
}

func saveRepositoryLanguageCache(cacheDir, path string, summary gitrepo.LanguageSummary, commit string) error {
	if cacheDir == "" {
		return nil
	}
	data, err := json.Marshal(repositoryLanguageCache{Version: gitrepo.LanguageAnalysisVersion, Summary: summary, Commit: commit})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(cacheDir, ".languages-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), repositoryLanguageCachePath(cacheDir, path))
}

func repositoryLanguageTooltip(summary gitrepo.LanguageSummary) string {
	var total int64
	for _, language := range summary.Languages {
		total += language.Bytes
	}
	parts := make([]string, 0, min(3, len(summary.Languages)))
	for _, language := range summary.Languages[:min(3, len(summary.Languages))] {
		text := language.Name
		if summary.Complete && total > 0 {
			text = fmt.Sprintf("%s %.1f%%", text, float64(language.Bytes)*100/float64(total))
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, " · ")
}

func (app *nativeApp) repositoryLanguageBadge(c *ui.Context, path string) {
	width, _ := c.Size()
	if app.languages == nil || width-app.prefs.Sidebar < 850 {
		return
	}
	summary := app.languages.entries[path]
	if len(summary.Languages) == 0 {
		return
	}
	language := summary.Languages[0]
	ui.Row(c).Key("repository-language").Width(132).Shrink(0).Gap(6).AlignItems(ui.Center).
		Tooltip(repositoryLanguageTooltip(summary)).Children(func() {
		ui.Box(c).Size(8, 8).Radius(4).Background(ui.Hex(language.Color)).Border(0.5, c.Theme().Border).Role(ui.RoleNone)
		ui.Text(c, language.Name).FontSize(12).TextColor(c.Theme().TextMuted).MinWidth(0).Shrink(1).MaxLines(1)
	})
}
