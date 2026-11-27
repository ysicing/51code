// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"
)

type nativeInsights struct {
	period     string
	ref        string
	requestRef string
	result     *gitrepo.RepositoryInsights
	from       time.Time
	until      time.Time
	checked    time.Time
	problem    string
}

type insightsCache struct {
	Key    string
	Saved  time.Time
	Result gitrepo.RepositoryInsights
}

func insightsDates(now time.Time, days int) (time.Time, time.Time) {
	return gitrepo.InsightsDayStart(now, 1-days), gitrepo.InsightsDayStart(now, 1)
}

func insightsCacheKey(path, ref string, source gitrepo.InsightsSourceInfo, from, until time.Time) string {
	// 提交 SHA 包含空提交；自然日及 UTC 边界同时参与键，时区/DST 变化不会复用旧桶。
	data, _ := json.Marshal([]any{1, path, ref, source, from.Format(time.RFC3339),
		until.Format(time.RFC3339), from.Location().String()})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func (app *nativeApp) insightsCacheFile(path string) string {
	if app.stateFile == "" {
		return ""
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	profile := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Dir(app.stateFile))))
	repository := fmt.Sprintf("%x.json", sha256.Sum256([]byte(path)))
	return filepath.Join(root, "51code", "repository-insights", profile[:16], repository)
}

func readInsightsCache(path, key string, from, until time.Time) *gitrepo.RepositoryInsights {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil
	}
	var cache insightsCache
	if json.Unmarshal(data, &cache) != nil || cache.Key != key ||
		time.Since(cache.Saved) < 0 || time.Since(cache.Saved) > 15*time.Minute ||
		len(cache.Result.Days) < 1 || len(cache.Result.Days) > 91 ||
		len(cache.Result.Authors) > 10 || len(cache.Result.Files) > 10 ||
		cache.Result.Commits < 0 || cache.Result.Commits > 20000 {
		return nil
	}
	result := &cache.Result
	date, commits, active := from, 0, 0
	for _, day := range result.Days {
		if day.Date != date.Format("2006-01-02") || !date.Before(until) ||
			day.Commits < 0 || day.Commits > 20000 || day.Added < 0 || day.Deleted < 0 {
			return nil
		}
		commits += day.Commits
		if day.Commits > 0 {
			active++
		}
		date = gitrepo.InsightsDayStart(date, 1)
	}
	if !date.Equal(until) || result.Commits != commits || result.ActiveDays != active ||
		result.Contributors < 0 || result.Contributors > commits ||
		result.ChangedFiles < 0 || result.ChangedFiles > 100000 ||
		len(result.Authors) != min(10, result.Contributors) || len(result.Files) != min(10, result.ChangedFiles) {
		return nil
	}
	for _, author := range result.Authors {
		if author.Commits <= 0 || author.Commits > commits {
			return nil
		}
	}
	for _, file := range result.Files {
		if file.Path == "" || file.Commits <= 0 || file.Commits > commits {
			return nil
		}
	}
	return &cache.Result
}

func writeInsightsCache(path, key string, result gitrepo.RepositoryInsights) {
	if path == "" {
		return
	}
	data, err := json.Marshal(insightsCache{Key: key, Saved: time.Now(), Result: result})
	if err != nil || os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".insights-*")
	if err != nil {
		return
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return
	}
	if file.Close() == nil {
		_ = os.Rename(file.Name(), path)
	}
}

// pruneInsightsCache 删除已不在仓库列表中的聚合缓存，每个仓库只保留一个文件。
func pruneInsightsCache(cacheFile string, paths []string) {
	if cacheFile == "" {
		return
	}
	directory := filepath.Dir(cacheFile)
	files, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	keep := make(map[string]bool, len(paths)+1)
	keep[filepath.Base(cacheFile)] = true
	for _, path := range paths {
		keep[fmt.Sprintf("%x.json", sha256.Sum256([]byte(path)))] = true
	}
	for _, file := range files {
		name := file.Name()
		// 只处理本功能生成的 <64 位十六进制>.json，目录中其他文件不碰。
		if file.IsDir() || keep[name] || len(name) != 69 || !strings.HasSuffix(name, ".json") {
			continue
		}
		if _, err := hex.DecodeString(strings.TrimSuffix(name, ".json")); err == nil {
			_ = os.Remove(filepath.Join(directory, name))
		}
	}
}

func (app *nativeApp) refreshInsights(force bool) {
	if app.repository == nil && app.ssh == nil || app.screen != "insights" || !app.prefs.SidebarItems.Insights || app.closed {
		return
	}
	if app.insights == nil {
		// 忙碌时也先建立页面状态，面板才能显示日期范围和筛选控件；忙碌结束后由 runTask 补读。
		from, until := insightsDates(time.Now(), 30)
		app.insights = &nativeInsights{period: "30", ref: "HEAD", from: from, until: until}
	}
	if app.busy || app.dialog != nil {
		return
	}
	state := app.insights
	days := 30
	if state.period == "7" {
		days = 7
	} else if state.period == "90" {
		days = 90
	}
	now := time.Now()
	from, until := insightsDates(now, days)
	if !force && state.ref == state.requestRef && from.Equal(state.from) && until.Equal(state.until) &&
		(app.readTask != nil || (state.result != nil || state.problem != "") && now.Sub(state.checked) < time.Minute) {
		return
	}
	if !from.Equal(state.from) || !until.Equal(state.until) {
		state.result = nil
	}
	state.from, state.until, state.checked, state.problem = from, until, now, ""
	state.requestRef = state.ref
	ref := state.ref
	var read func(context.Context) (*gitrepo.RepositoryInsights, error)
	if app.ssh != nil {
		repo := app.ssh.repo
		read = func(ctx context.Context) (*gitrepo.RepositoryInsights, error) {
			result, err := repo.Insights(ctx, ref, from, until)
			return &result, err
		}
	} else {
		path := app.repository.snapshot.Path
		cacheFile := app.insightsCacheFile(path)
		known := make([]string, 0, len(app.library.Repositories))
		for _, entry := range app.library.Repositories {
			known = append(known, entry.Path)
		}
		read = func(ctx context.Context) (*gitrepo.RepositoryInsights, error) {
			source, err := app.service.InsightsSource(ctx, path, ref)
			if err != nil {
				return nil, err
			}
			key := insightsCacheKey(path, ref, source, from, until)
			if !force {
				if result := readInsightsCache(cacheFile, key, from, until); result != nil {
					return result, nil
				}
			}
			result, err := app.service.Insights(ctx, path, source, from, until)
			if err == nil && ctx.Err() == nil {
				pruneInsightsCache(cacheFile, known)
				writeInsightsCache(cacheFile, key, result)
			}
			return &result, err
		}
	}
	app.load(locales.InsightsLoading(), nativeReadInsights, func(ctx context.Context) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		result, err := read(ctx)
		return func() {
			if app.insights != state || state.ref != ref || !state.from.Equal(from) || !state.until.Equal(until) {
				return
			}
			if err != nil {
				state.problem = err.Error()
			} else if result != nil {
				state.result = result
			}
		}, nil
	})
}

func (app *nativeApp) insightsRefs() []string {
	refs := []string{"HEAD"}
	if app.ssh != nil {
		for _, ref := range app.ssh.refs {
			if strings.HasPrefix(ref, "refs/heads/") {
				refs = append(refs, ref)
			}
		}
	} else {
		for _, branch := range app.repository.branches {
			if !branch.Remote {
				refs = append(refs, "refs/heads/"+branch.Name)
			}
		}
	}
	if app.insights != nil && !slices.Contains(refs, app.insights.ref) {
		refs = append(refs, app.insights.ref)
	}
	return refs
}
