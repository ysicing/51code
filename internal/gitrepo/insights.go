// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const insightsTokenLimit = 1 << 20

// InsightsSourceInfo 固定分析输入；浅克隆边界和工作树 mailmap 的变化也必须使缓存失效。
type InsightsSourceInfo struct {
	Commit  string
	Mailmap string
	Shallow string
}

type InsightsDay struct {
	Date    string
	Commits int
	Added   int64
	Deleted int64
}

type InsightsAuthor struct {
	Name    string
	Email   string
	Commits int
}

type InsightsFile struct {
	Path    string
	Commits int
}

// RepositoryInsights 仅包含聚合数据，不持久化提交正文和完整文件清单。
type RepositoryInsights struct {
	Commits      int
	Contributors int
	ActiveDays   int
	ChangedFiles int
	Shallow      bool
	Days         []InsightsDay
	Authors      []InsightsAuthor
	Files        []InsightsFile
}

func insightsFingerprint(path string) (string, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, insightsTokenLimit+1))
	if err != nil {
		return "", err
	}
	if len(data) > insightsTokenLimit {
		return "", errors.New("洞察身份文件超过 1 MiB")
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func (repo *Repository) InsightsSource(ctx context.Context, ref string) (InsightsSourceInfo, error) {
	var source InsightsSourceInfo
	value, err := repo.git(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		if ref != "HEAD" {
			return source, err
		}
		// 仅将确实没有任何提交的 HEAD 作为空仓库，失效引用仍报告错误。
		symbolic, symbolicErr := repo.git(ctx, "symbolic-ref", "-q", "HEAD")
		if symbolicErr != nil {
			return source, err
		}
		if _, verifyErr := repo.git(ctx, "show-ref", "--verify", "--quiet", strings.TrimSpace(symbolic)); !exitCode(verifyErr, 1) {
			return source, err
		}
	} else {
		source.Commit = strings.TrimSpace(value)
	}
	source.Mailmap, err = insightsFingerprint(filepath.Join(repo.Path, ".mailmap"))
	if err != nil {
		return source, err
	}
	shallowPath, err := repo.git(ctx, "rev-parse", "--git-path", "shallow")
	if err != nil {
		return source, err
	}
	shallowPath = strings.TrimSpace(shallowPath)
	if !filepath.IsAbs(shallowPath) {
		shallowPath = filepath.Join(repo.Path, shallowPath)
	}
	source.Shallow, err = insightsFingerprint(shallowPath)
	return source, err
}

// Insights 以本地自然日分桶，使用提交者时间筛选，合并提交不计入任何指标。
func (repo *Repository) Insights(ctx context.Context, source InsightsSourceInfo, from, until time.Time) (RepositoryInsights, error) {
	result, err := calculateInsights(ctx, source, from, until, func(ctx context.Context, stdin string, stdout io.Writer, args ...string) error {
		return runTo(ctx, repo.Path, stdin, 20*time.Second, stdout, func() string { return "" }, args...)
	})
	if err != nil {
		return RepositoryInsights{}, err
	}
	mailmap, err := insightsFingerprint(filepath.Join(repo.Path, ".mailmap"))
	if err != nil || mailmap != source.Mailmap {
		return RepositoryInsights{}, errors.New("作者映射已变化，请刷新洞察")
	}
	return result, nil
}

func calculateInsights(ctx context.Context, source InsightsSourceInfo, from, until time.Time,
	run func(context.Context, string, io.Writer, ...string) error) (RepositoryInsights, error) {
	if err := ctx.Err(); err != nil {
		return RepositoryInsights{}, err
	}
	if !until.After(from) || until.Sub(from) > 92*24*time.Hour {
		return RepositoryInsights{}, errors.New("洞察日期范围无效")
	}
	parser := newInsightsParser(from, until)
	if source.Commit != "" {
		if err := validCommitHash(source.Commit); err != nil {
			return RepositoryInsights{}, err
		}
		// 固定 mailmap 来源，避免外部配置文件悄悄改变同一缓存键的作者统计。
		logArgs := []string{"-c", "mailmap.file=", "-c", "mailmap.blob=", "-c", "log.showSignature=false",
			"log", "--format=%x1e%H%x00%ct%x00%aN%x00%aE%x00", "--numstat", "-z",
			"--no-merges", "--no-ext-diff", "--no-textconv", "--find-renames", "-l200"}
		err := run(ctx, "", parser, append(slices.Clip(logArgs),
			"--since-as-filter="+from.Format(time.RFC3339), "--until="+until.Format(time.RFC3339),
			"--max-count=20001", source.Commit, "--")...)
		// Git 2.37 之前没有 --since-as-filter（如 Ubuntu 22.04 的 2.34）。不能退回 --since：
		// 它遇到早于起点的提交就把祖先标记为不感兴趣，提交时间倒置的历史会被静默漏算。
		if err != nil && ctx.Err() == nil && strings.Contains(err.Error(), "since-as-filter") {
			parser = newInsightsParser(from, until)
			var commits string
			commits, err = insightsCommits(ctx, source.Commit, from, until, run)
			// 空列表必须跳过：log --stdin 没有输入时会退回显示 HEAD。
			if err == nil && commits != "" {
				err = run(ctx, commits, parser,
					append(slices.Clip(logArgs), "--no-walk=unsorted", "--stdin", "--")...)
			}
		}
		if err != nil {
			return RepositoryInsights{}, err
		}
	}
	if len(parser.pending) != 0 || parser.field != 0 || parser.rename != 0 {
		return RepositoryInsights{}, errors.New("洞察 Git 输出不完整")
	}
	result := parser.result()
	result.Shallow = source.Shallow != ""
	return result, nil
}

// insightsCommits 为旧版 Git 完整遍历可达提交（不计算 diff），按提交者时间过滤后返回换行分隔的 SHA。
// 遍历受 20 秒超时约束，范围内超过 20000 条时报错，不返回部分列表。
func insightsCommits(ctx context.Context, commit string, from, until time.Time,
	run func(context.Context, string, io.Writer, ...string) error) (string, error) {
	list := &insightsRevisions{from: from.Unix(), until: until.Unix()}
	err := run(ctx, "", list,
		"rev-list", "--no-merges", "--timestamp", commit, "--")
	if err != nil {
		return "", err
	}
	if len(list.pending) != 0 {
		return "", errors.New("洞察 Git 输出不完整")
	}
	return list.hashes.String(), nil
}

// insightsRevisions 流式解析 rev-list --timestamp 的「时间戳 SHA」行，只保留范围内的 SHA。
type insightsRevisions struct {
	from, until int64
	pending     []byte
	hashes      strings.Builder
	count       int
}

func (list *insightsRevisions) Write(data []byte) (int, error) {
	size := len(data)
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')
		if index < 0 {
			if len(list.pending)+len(data) > 256 {
				return 0, errors.New("洞察提交列表格式无效")
			}
			list.pending = append(list.pending, data...)
			break
		}
		line := string(append(list.pending, data[:index]...))
		list.pending, data = list.pending[:0], data[index+1:]
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return 0, errors.New("洞察提交列表格式无效")
		}
		stamp, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, errors.New("洞察提交时间无效")
		}
		if stamp < list.from || stamp >= list.until {
			continue
		}
		if err := validCommitHash(fields[1]); err != nil {
			return 0, err
		}
		if list.count++; list.count > 20000 {
			return 0, errors.New("洞察超过 20000 条提交，请缩短日期范围")
		}
		list.hashes.WriteString(fields[1])
		list.hashes.WriteByte('\n')
	}
	return size, nil
}

// InsightsDayStart 返回 date 所在自然日之后第 offset 天在同一时区的起点。
// 午夜可能落在夏令时跳变缺口内（如 America/Santiago 的 9 月），time.Date 会退回前一天 23:00，
// 此时当天实际从跳变时刻开始。逐日推进必须按日历字段计算，不能对退回的时刻继续 AddDate，否则会产生重复日期。
func InsightsDayStart(date time.Time, offset int) time.Time {
	year, month, day := time.Date(date.Year(), date.Month(), date.Day()+offset, 12, 0, 0, 0, time.UTC).Date()
	start := time.Date(year, month, day, 0, 0, 0, 0, date.Location())
	if y, m, d := start.Date(); y != year || m != month || d != day {
		_, start = start.ZoneBounds()
	}
	return start
}

type insightsParser struct {
	from, until time.Time
	pending     []byte
	bytes       int
	field       int
	headers     int
	rename      int
	current     bool
	day         int
	author      string
	added       int64
	deleted     int64
	days        []InsightsDay
	dayIndex    map[string]int
	authors     map[string]InsightsAuthor
	files       map[string]int
	touched     map[string]bool
}

func newInsightsParser(from, until time.Time) *insightsParser {
	parser := &insightsParser{from: from, until: until, dayIndex: map[string]int{},
		authors: map[string]InsightsAuthor{}, files: map[string]int{}, touched: map[string]bool{}}
	for date := from; date.Before(until); date = InsightsDayStart(date, 1) {
		label := date.Format("2006-01-02")
		parser.dayIndex[label] = len(parser.days)
		parser.days = append(parser.days, InsightsDay{Date: label})
	}
	return parser
}

func (parser *insightsParser) Write(data []byte) (int, error) {
	size := len(data)
	parser.bytes += size
	if parser.bytes > 32<<20 {
		return 0, errors.New("洞察扫描超过 32 MiB，请缩短日期范围")
	}
	for len(data) > 0 {
		index := bytes.IndexByte(data, 0)
		if index < 0 {
			index = len(data)
		}
		if len(parser.pending)+index > insightsTokenLimit {
			return 0, errors.New("洞察记录超过 1 MiB")
		}
		parser.pending = append(parser.pending, data[:index]...)
		data = data[index:]
		if len(data) == 0 {
			break
		}
		if err := parser.token(string(parser.pending)); err != nil {
			return 0, err
		}
		parser.pending = parser.pending[:0]
		data = data[1:]
	}
	return size, nil
}

func (parser *insightsParser) token(token string) error {
	if parser.rename != 0 {
		parser.rename--
		if parser.rename == 0 {
			parser.file(token)
		}
		if len(parser.files) > 100000 {
			return errors.New("洞察聚合超过限制，请缩短日期范围")
		}
		return nil
	}
	if parser.field != 0 {
		switch parser.field {
		case 1:
			stamp, err := strconv.ParseInt(token, 10, 64)
			if err != nil {
				return err
			}
			date := time.Unix(stamp, 0).In(parser.from.Location())
			parser.day, parser.current = parser.dayIndex[date.Format("2006-01-02")]
			parser.current = parser.current && !date.Before(parser.from) && date.Before(parser.until)
		case 2:
			parser.author = token
		case 3:
			if parser.current {
				key := strings.ToLower(token)
				if key == "" {
					key = parser.author
				}
				author := parser.authors[key]
				author.Name, author.Email, author.Commits = parser.author, token, author.Commits+1
				parser.authors[key] = author
				parser.days[parser.day].Commits++
			}
		}
		parser.field = (parser.field + 1) % 4
		return nil
	}
	token = strings.TrimLeft(token, "\n")
	if token == "" {
		return nil
	}
	if strings.HasPrefix(token, "\x1e") {
		parser.headers++
		if parser.headers > 20000 {
			return errors.New("洞察超过 20000 条提交，请缩短日期范围")
		}
		clear(parser.touched)
		parser.field = 1
		return nil
	}
	fields := strings.SplitN(token, "\t", 3)
	if len(fields) != 3 || parser.headers == 0 {
		return errors.New("洞察 numstat 格式无效")
	}
	var err error
	parser.added, parser.deleted = 0, 0
	if fields[0] != "-" || fields[1] != "-" {
		parser.added, err = strconv.ParseInt(fields[0], 10, 64)
		if err != nil || parser.added < 0 {
			return errors.New("洞察新增行数无效")
		}
		parser.deleted, err = strconv.ParseInt(fields[1], 10, 64)
		if err != nil || parser.deleted < 0 {
			return errors.New("洞察删除行数无效")
		}
	}
	if fields[2] == "" {
		parser.rename = 2
	} else {
		parser.file(fields[2])
	}
	if len(parser.files) > 100000 || len(parser.authors) > 20000 {
		return errors.New("洞察聚合超过限制，请缩短日期范围")
	}
	return nil
}

func (parser *insightsParser) file(path string) {
	if !parser.current || parser.touched[path] {
		return
	}
	parser.touched[path] = true
	parser.files[path]++
	parser.days[parser.day].Added += parser.added
	parser.days[parser.day].Deleted += parser.deleted
}

func (parser *insightsParser) result() RepositoryInsights {
	result := RepositoryInsights{Days: parser.days, Contributors: len(parser.authors), ChangedFiles: len(parser.files)}
	for _, day := range result.Days {
		result.Commits += day.Commits
		if day.Commits > 0 {
			result.ActiveDays++
		}
	}
	for _, author := range parser.authors {
		result.Authors = append(result.Authors, author)
	}
	sort.Slice(result.Authors, func(first, second int) bool {
		left, right := result.Authors[first], result.Authors[second]
		if left.Commits != right.Commits {
			return left.Commits > right.Commits
		}
		return left.Email < right.Email
	})
	for path, count := range parser.files {
		result.Files = append(result.Files, InsightsFile{Path: path, Commits: count})
	}
	sort.Slice(result.Files, func(first, second int) bool {
		left, right := result.Files[first], result.Files[second]
		if left.Commits != right.Commits {
			return left.Commits > right.Commits
		}
		return left.Path < right.Path
	})
	result.Authors = result.Authors[:min(10, len(result.Authors))]
	result.Files = result.Files[:min(10, len(result.Files))]
	return result
}
