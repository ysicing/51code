// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-enry/go-enry/v2"
)

const LanguageAnalysisVersion = "enry-2.9.6-v1"

const (
	languageMaxBlob  = 1 << 20
	languageMaxBytes = 32 << 20
	languageMaxFiles = 20000
	languageSample   = 64 << 10
	languageTimeout  = 15 * time.Second
)

type LanguageStat struct {
	Name  string `json:"name"`
	Color string `json:"color"`
	Bytes int64  `json:"bytes"`
}

type LanguageSummary struct {
	Tree      string         `json:"tree"`
	Languages []LanguageStat `json:"languages"`
	Complete  bool           `json:"complete"`
}

func (repo *Repository) LanguageTree(ctx context.Context) (string, error) {
	output, err := repo.git(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{tree}")
	if exitCode(err, 1) {
		return "", nil
	}
	return strings.TrimSpace(output), err
}

func (repo *Repository) Languages(ctx context.Context, tree string) (LanguageSummary, error) {
	result := LanguageSummary{Tree: tree, Complete: true}
	if len(tree) != 40 && len(tree) != 64 {
		return result, errors.New("无效的 Git tree 对象")
	}
	if _, err := hex.DecodeString(tree); err != nil {
		return result, errors.New("无效的 Git tree 对象")
	}
	ctx, cancel := context.WithTimeout(ctx, languageTimeout)
	defer cancel()
	treeWriter := &languageTreeWriter{ctx: ctx, complete: true}
	if err := runTo(ctx, repo.Path, "", languageTimeout, treeWriter, func() string { return "" }, "ls-tree", "-r", "-l", "-z", tree, "--"); err != nil {
		return result, err
	}
	if len(treeWriter.record) != 0 {
		return result, errors.New("Git tree 内容不完整")
	}
	result.Complete = treeWriter.complete
	blobs := treeWriter.blobs
	var input strings.Builder
	for _, blob := range blobs {
		input.WriteString(blob.object + "\n")
	}
	if len(blobs) == 0 {
		return result, nil
	}
	// 使用一次 cat-file 批量读取已固定 tree 的 blob；只保留每个文件的有限样本，
	// 统计仍使用完整对象大小，不读取工作区、符号链接目标或子模块。
	writer := &languageBatchWriter{ctx: ctx, blobs: blobs, totals: map[string]int64{}}
	if err := runTo(ctx, repo.Path, input.String(), languageTimeout, writer, func() string { return "" }, "cat-file", "--batch"); err != nil {
		return result, err
	}
	if writer.index != len(blobs) || len(writer.header) != 0 || writer.body {
		return result, errors.New("Git blob 内容不完整")
	}
	for name, size := range writer.totals {
		result.Languages = append(result.Languages, LanguageStat{Name: name, Color: enry.GetColor(name), Bytes: size})
	}
	slices.SortFunc(result.Languages, func(left, right LanguageStat) int {
		if left.Bytes > right.Bytes {
			return -1
		}
		if left.Bytes < right.Bytes {
			return 1
		}
		return strings.Compare(left.Name, right.Name)
	})
	return result, nil
}

func languageByName(path string) (string, bool) {
	if language, safe := enry.GetLanguageByFilename(path); safe {
		return language, true
	}
	return enry.GetLanguageByExtension(path)
}

func sourceLanguage(language string) bool {
	kind := enry.GetLanguageType(language)
	return kind == enry.Programming || kind == enry.Markup
}

type languageBlob struct {
	path   string
	object string
	size   int64
}

type languageTreeWriter struct {
	ctx           context.Context
	record        []byte
	blobs         []languageBlob
	total         int64
	metadataBytes int
	complete      bool
}

func (writer *languageTreeWriter) Write(data []byte) (int, error) {
	size := len(data)
	for len(data) > 0 {
		if err := writer.ctx.Err(); err != nil {
			return 0, err
		}
		end := bytes.IndexByte(data, 0)
		length := len(data)
		if end >= 0 {
			length = end
		}
		if len(writer.record)+length > 64<<10 {
			return 0, errors.New("单条 Git tree 记录过长")
		}
		writer.record = append(writer.record, data[:length]...)
		data = data[length:]
		if end < 0 {
			break
		}
		if err := writer.accept(string(writer.record)); err != nil {
			return 0, err
		}
		writer.record = writer.record[:0]
		data = data[1:]
	}
	return size, nil
}

func (writer *languageTreeWriter) accept(record string) error {
	metadata, path, ok := strings.Cut(record, "\t")
	fields := strings.Fields(metadata)
	if !ok || len(fields) != 4 {
		return errors.New("无法解析 Git tree")
	}
	if fields[0] != "100644" && fields[0] != "100755" || fields[1] != "blob" ||
		enry.IsVendor(path) || enry.IsDocumentation(path) || enry.IsGenerated(path, nil) {
		return nil
	}
	language, safe := languageByName(path)
	if safe && !sourceLanguage(language) {
		return nil
	}
	size, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || size < 0 {
		return errors.New("无效的 Git blob 大小")
	}
	if size == 0 {
		return nil
	}
	if size > languageMaxBlob || writer.total+size > languageMaxBytes ||
		len(writer.blobs) >= languageMaxFiles || writer.metadataBytes+len(record) > outputLimit {
		writer.complete = false
		return nil
	}
	writer.blobs = append(writer.blobs, languageBlob{path: path, object: fields[2], size: size})
	writer.total += size
	writer.metadataBytes += len(record)
	return nil
}

type languageBatchWriter struct {
	ctx       context.Context
	blobs     []languageBlob
	index     int
	header    []byte
	body      bool
	remaining int64
	sample    []byte
	totals    map[string]int64
}

func (writer *languageBatchWriter) Write(data []byte) (int, error) {
	size := len(data)
	for len(data) > 0 {
		if err := writer.ctx.Err(); err != nil {
			return 0, err
		}
		if writer.index >= len(writer.blobs) {
			return 0, errors.New("Git 返回了额外的 blob 内容")
		}
		blob := writer.blobs[writer.index]
		if !writer.body {
			end := bytes.IndexByte(data, '\n')
			length := len(data)
			if end >= 0 {
				length = end
			}
			if len(writer.header)+length > 128 {
				return 0, errors.New("无效的 Git blob 响应头")
			}
			writer.header = append(writer.header, data[:length]...)
			data = data[length:]
			if end < 0 {
				break
			}
			if string(writer.header) != fmt.Sprintf("%s blob %d", blob.object, blob.size) {
				return 0, errors.New("Git blob 与 tree 不一致")
			}
			writer.header = writer.header[:0]
			writer.body, writer.remaining = true, blob.size
			data = data[1:]
		}
		if writer.remaining > 0 {
			length := int(min(int64(len(data)), writer.remaining))
			retained := min(length, languageSample-len(writer.sample))
			writer.sample = append(writer.sample, data[:retained]...)
			writer.remaining -= int64(length)
			data = data[length:]
		}
		if writer.remaining == 0 && len(data) > 0 {
			if data[0] != '\n' {
				return 0, errors.New("无效的 Git blob 结束符")
			}
			writer.accept(blob)
			writer.index++
			writer.body = false
			writer.sample = writer.sample[:0]
			data = data[1:]
		}
	}
	return size, nil
}

func (writer *languageBatchWriter) accept(blob languageBlob) {
	if enry.IsBinary(writer.sample) || enry.IsGenerated(blob.path, writer.sample) {
		return
	}
	language, safe := languageByName(blob.path)
	if !safe {
		language = enry.GetLanguage(blob.path, writer.sample)
	}
	if !sourceLanguage(language) {
		return
	}
	if group := enry.GetLanguageGroup(language); group != "" {
		language = group
	}
	writer.totals[language] += blob.size
}
