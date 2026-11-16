// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const inspectionSampleLimit = 8192

type FileInspection struct {
	Path        string
	Size        int64
	Binary      bool
	LFS         bool
	Pointer     bool
	Fingerprint string
}

type FileInspectionReport struct {
	Files []FileInspection
	Token string
}

func (repo *Repository) InspectFiles(ctx context.Context, paths []string) (FileInspectionReport, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	names, err := repo.changedPaths(ctx, paths, false)
	if err != nil {
		return FileInspectionReport{}, err
	}
	files, err := repo.inspectWorktreeFiles(ctx, names)
	if err != nil {
		return FileInspectionReport{}, err
	}
	// 工作区检查不要求索引可写成树，允许其他路径仍存在未解决冲突。
	token, err := runDigest(ctx, repo.Path, 30*time.Second, "ls-files", "--stage", "-z")
	if err != nil {
		return FileInspectionReport{}, err
	}
	return FileInspectionReport{Files: files, Token: token}, nil
}

func (repo *Repository) InspectStagedFiles(ctx context.Context) (FileInspectionReport, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return repo.inspectStagedFiles(ctx)
}

func (repo *Repository) StageInspected(ctx context.Context, paths []string, expected FileInspectionReport) (FileInspectionReport, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	names, err := repo.changedPaths(ctx, paths, false)
	if err != nil {
		return FileInspectionReport{}, err
	}
	files, err := repo.inspectWorktreeFiles(ctx, names)
	if err != nil {
		return FileInspectionReport{}, err
	}
	if !sameInspectedFiles(files, expected.Files) {
		return FileInspectionReport{}, errors.New("工作区文件已变化，请重新检查后暂存")
	}
	currentIndex, err := runDigest(ctx, repo.Path, 30*time.Second, "ls-files", "--stage", "-z")
	if err != nil {
		return FileInspectionReport{}, err
	}
	if expected.Token == "" || currentIndex != expected.Token {
		return FileInspectionReport{}, errors.New("暂存区已变化，请重新检查后暂存")
	}
	if _, err := run(ctx, repo.Path, "", 2*time.Minute, append([]string{"add", "--"}, names...)...); err != nil {
		return FileInspectionReport{}, err
	}
	unmerged, err := repo.git(ctx, "ls-files", "--unmerged", "-z")
	if err != nil {
		return FileInspectionReport{}, err
	}
	var report FileInspectionReport
	if unmerged == "" {
		report, err = repo.inspectStagedFiles(ctx)
		if err != nil {
			return FileInspectionReport{}, err
		}
	}
	// 暂存后文件可能已不在 status 中；仍检查原路径，拒绝确认过滤器或外部编辑产生的新内容。
	files, err = repo.inspectWorktreeFiles(ctx, names)
	if err != nil {
		return FileInspectionReport{}, err
	}
	if !sameInspectedFiles(files, expected.Files) {
		return FileInspectionReport{}, errors.New("暂存期间工作区文件已变化，请重新检查")
	}
	if unmerged != "" {
		// 暂存成功但索引尚不可提交；完成工作区复核后返回空报告，不确认任何 blob 风险。
		return FileInspectionReport{}, nil
	}
	token, err := repo.commitToken(ctx)
	if err != nil {
		return FileInspectionReport{}, err
	}
	if token != report.Token {
		return FileInspectionReport{}, errors.New("暂存区已变化，请重新检查")
	}
	return report, nil
}

func (repo *Repository) CommitInspected(ctx context.Context, message, token string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("请输入提交说明")
	}
	if len(message) > 64<<10 {
		return errors.New("提交说明过长")
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	current, err := repo.commitToken(ctx)
	if err != nil {
		return err
	}
	if token == "" || current != token {
		return errors.New("暂存区或当前提交已变化，请重新检查")
	}
	_, err = run(ctx, repo.Path, message, 2*time.Minute, "commit", "--file=-")
	return err
}

func sameInspectedFiles(current, expected []FileInspection) bool {
	if len(current) != len(expected) {
		return false
	}
	known := make(map[string]FileInspection, len(expected))
	for _, file := range expected {
		if _, exists := known[file.Path]; exists {
			return false
		}
		known[file.Path] = file
	}
	for _, file := range current {
		if previous, exists := known[file.Path]; !exists || previous != file {
			return false
		}
		delete(known, file.Path)
	}
	return len(known) == 0
}

type inspectionAttributes struct {
	diff   string
	filter string
}

func (repo *Repository) inspectionAttributes(ctx context.Context, paths []string, cached bool) (map[string]inspectionAttributes, error) {
	result := make(map[string]inspectionAttributes, len(paths))
	if len(paths) == 0 {
		return result, nil
	}
	args := []string{"check-attr", "-z", "--stdin"}
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "diff", "filter")
	output, err := run(ctx, repo.Path, strings.Join(paths, "\x00")+"\x00", 30*time.Second, args...)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(fields) != len(paths)*6 {
		return nil, errors.New("无法解析文件属性")
	}
	for index := 0; index < len(fields); index += 3 {
		path, attribute, value := fields[index], fields[index+1], fields[index+2]
		entry := result[path]
		switch attribute {
		case "diff":
			entry.diff = value
		case "filter":
			entry.filter = value
		default:
			return nil, errors.New("无法解析文件属性")
		}
		result[path] = entry
	}
	return result, nil
}

func (repo *Repository) inspectionBinary(ctx context.Context, attribute string, sample []byte, drivers map[string]*bool) (bool, error) {
	switch attribute {
	case "unset":
		return true, nil
	case "set":
		return false, nil
	case "", "unspecified":
		return bytes.IndexByte(sample, 0) >= 0, nil
	}
	if binary, exists := drivers[attribute]; exists {
		if binary != nil {
			return *binary, nil
		}
		return bytes.IndexByte(sample, 0) >= 0, nil
	}
	output, err := repo.git(ctx, "config", "--type=bool", "--get", "diff."+attribute+".binary")
	if exitCode(err, 1) {
		drivers[attribute] = nil
		return bytes.IndexByte(sample, 0) >= 0, nil
	}
	if err != nil {
		return false, err
	}
	binary, err := strconv.ParseBool(strings.TrimSpace(output))
	if err != nil {
		return false, errors.New("差异驱动的二进制配置无效")
	}
	drivers[attribute] = &binary
	return binary, nil
}

func (repo *Repository) inspectWorktreeFiles(ctx context.Context, paths []string) ([]FileInspection, error) {
	attributes, err := repo.inspectionAttributes(ctx, paths, false)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(repo.Path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files := make([]FileInspection, 0, len(paths))
	drivers := make(map[string]*bool)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := repo.checkConflictAncestors(ctx, root, path); err != nil {
			if errors.Is(err, errUnsupportedAncestor) {
				continue
			}
			return nil, err
		}
		info, err := root.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		regular := info.Mode().IsRegular()
		if !regular && info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		var sample []byte
		if regular {
			sample, err = inspectWorktreeSample(root, path, info)
		} else {
			// 链接只检查自身文本，绝不打开目标；损坏链接也可正常暂存。
			var target string
			target, err = root.Readlink(path)
			sample = []byte(target[:min(len(target), inspectionSampleLimit)])
		}
		if err != nil {
			return nil, err
		}
		current, err := root.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !os.SameFile(info, current) || info.Size() != current.Size() ||
			info.Mode() != current.Mode() || !info.ModTime().Equal(current.ModTime()) {
			return nil, fmt.Errorf("读取时文件已变化，请刷新：%s", path)
		}
		attribute := attributes[path]
		binary, err := repo.inspectionBinary(ctx, attribute.diff, sample, drivers)
		if err != nil {
			return nil, err
		}
		file := FileInspection{
			Path: path, Size: info.Size(), Binary: binary, LFS: attribute.filter == "lfs",
			Pointer: regular && info.Size() == int64(len(sample)) && isLFSPointer(sample),
		}
		// 指纹只绑定文件元数据和有限样本，允许 LFS 调用方在插入属性规则后复核同一内容。
		digest := sha256.New()
		fmt.Fprintf(digest, "%d\x00%d\x00%d\x00", info.Size(), info.Mode(), info.ModTime().UnixNano())
		_, _ = digest.Write(sample)
		file.Fingerprint = hex.EncodeToString(digest.Sum(nil))
		files = append(files, file)
	}
	return files, nil
}

func inspectWorktreeSample(root *os.Root, path string, info os.FileInfo) ([]byte, error) {
	file, err := openFileContent(root, path, info)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	sample, err := io.ReadAll(io.LimitReader(file, inspectionSampleLimit))
	if err != nil {
		return nil, err
	}
	current, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if current.Size() != info.Size() || current.Mode() != info.Mode() || !current.ModTime().Equal(info.ModTime()) {
		return nil, fmt.Errorf("读取时文件已变化，请刷新：%s", path)
	}
	return sample, nil
}

type inspectionIndexEntry struct {
	path string
	mode string
	oid  string
}

func (repo *Repository) inspectStagedFiles(ctx context.Context) (FileInspectionReport, error) {
	token, err := repo.commitToken(ctx)
	if err != nil {
		return FileInspectionReport{}, err
	}
	head, _, _ := strings.Cut(token, "\n")
	args := []string{"diff", "--cached", "--name-only", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--diff-filter=ACMT"}
	if head != "" {
		args = append(args, head)
	}
	output, err := repo.git(ctx, append(args, "--")...)
	if err != nil {
		return FileInspectionReport{}, err
	}
	selected := make(map[string]bool)
	for _, path := range strings.Split(output, "\x00") {
		if path != "" {
			selected[path] = true
		}
	}
	entries := []inspectionIndexEntry{}
	paths := []string{}
	if len(selected) != 0 {
		output, err = repo.git(ctx, "ls-files", "--stage", "-z")
		if err != nil {
			return FileInspectionReport{}, err
		}
		for _, record := range strings.Split(output, "\x00") {
			metadata, path, ok := strings.Cut(record, "\t")
			if !ok || !selected[path] {
				continue
			}
			fields := strings.Fields(metadata)
			if len(fields) != 3 || fields[2] != "0" {
				return FileInspectionReport{}, errors.New("索引文件没有唯一暂存版本")
			}
			if fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000" {
				continue
			}
			if err := validPath(path); err != nil {
				return FileInspectionReport{}, err
			}
			if err := validCommitHash(fields[1]); err != nil {
				return FileInspectionReport{}, err
			}
			entries = append(entries, inspectionIndexEntry{path: path, mode: fields[0], oid: fields[1]})
			paths = append(paths, path)
		}
	}
	sizes, err := repo.inspectionBlobSizes(ctx, entries)
	if err != nil {
		return FileInspectionReport{}, err
	}
	attributes, err := repo.inspectionAttributes(ctx, paths, true)
	if err != nil {
		return FileInspectionReport{}, err
	}
	files := make([]FileInspection, 0, len(entries))
	drivers := make(map[string]*bool)
	for _, entry := range entries {
		size := sizes[entry.oid]
		sample, err := repo.inspectionBlobSample(ctx, entry.oid, size)
		if err != nil {
			return FileInspectionReport{}, err
		}
		attribute := attributes[entry.path]
		binary, err := repo.inspectionBinary(ctx, attribute.diff, sample, drivers)
		if err != nil {
			return FileInspectionReport{}, err
		}
		files = append(files, FileInspection{
			Path: entry.path, Size: size, Binary: binary, LFS: attribute.filter == "lfs",
			Pointer:     entry.mode != "120000" && size == int64(len(sample)) && isLFSPointer(sample),
			Fingerprint: entry.oid,
		})
	}
	current, err := repo.commitToken(ctx)
	if err != nil {
		return FileInspectionReport{}, err
	}
	if current != token {
		return FileInspectionReport{}, errors.New("暂存区已变化，请重新检查")
	}
	return FileInspectionReport{Files: files, Token: token}, nil
}

func (repo *Repository) inspectionBlobSizes(ctx context.Context, entries []inspectionIndexEntry) (map[string]int64, error) {
	result := make(map[string]int64, len(entries))
	if len(entries) == 0 {
		return result, nil
	}
	var input strings.Builder
	for _, entry := range entries {
		input.WriteString(entry.oid)
		input.WriteByte('\n')
	}
	output, err := run(ctx, repo.Path, input.String(), 30*time.Second, "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != len(entries) {
		return nil, errors.New("无法解析暂存对象大小")
	}
	for index, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != entries[index].oid || fields[1] != "blob" {
			return nil, errors.New("暂存文件对象无效")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return nil, errors.New("暂存文件对象大小无效")
		}
		result[fields[0]] = size
	}
	return result, nil
}

type inspectionSampleWriter struct {
	sample   []byte
	limit    int
	received int64
	cancel   context.CancelFunc
}

func (writer *inspectionSampleWriter) Write(data []byte) (int, error) {
	count := len(data)
	writer.received += int64(count)
	remaining := writer.limit - len(writer.sample)
	writer.sample = append(writer.sample, data[:min(remaining, len(data))]...)
	if len(writer.sample) == writer.limit && writer.cancel != nil {
		writer.cancel()
	}
	return count, nil
}

func (repo *Repository) inspectionBlobSample(ctx context.Context, oid string, size int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	sampleCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	writer := inspectionSampleWriter{limit: int(min(size, int64(inspectionSampleLimit)))}
	if size > inspectionSampleLimit {
		// 返回写错误不能保证子进程退出；取足样本后取消整个 Git 进程组，避免排空巨大 blob。
		writer.cancel = cancel
	}
	err := runTo(sampleCtx, repo.Path, "", 30*time.Second, &writer, func() string { return "" },
		"cat-file", "blob", oid)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if size > inspectionSampleLimit && len(writer.sample) == inspectionSampleLimit {
		return writer.sample, nil
	}
	if err != nil {
		return nil, err
	}
	if writer.received != size || int64(len(writer.sample)) != size {
		return nil, errors.New("暂存文件对象内容不完整")
	}
	return writer.sample, nil
}

func isLFSPointer(content []byte) bool {
	if len(content) == 0 || len(content) >= 1024 {
		return false
	}
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n"), "\n")
	if len(lines) < 3 {
		return false
	}
	switch lines[0] {
	case "version https://git-lfs.github.com/spec/v1", "version https://hawser.github.com/spec/v1", "version http://git-media.io/v/2":
	default:
		return false
	}
	priorities := make(map[byte]bool)
	index := 1
	for index < len(lines) && strings.HasPrefix(lines[index], "ext-") {
		key, value, ok := strings.Cut(lines[index], " ")
		if !ok || len(key) < 7 || key[4] < '0' || key[4] > '9' || key[5] != '-' ||
			priorities[key[4]] || !validInspectionPointerOID(value) {
			return false
		}
		for _, letter := range key[6:] {
			if letter != '_' && (letter < 'a' || letter > 'z') && (letter < 'A' || letter > 'Z') &&
				(letter < '0' || letter > '9') {
				return false
			}
		}
		priorities[key[4]] = true
		index++
	}
	if len(lines)-index != 2 {
		return false
	}
	oid, ok := strings.CutPrefix(lines[index], "oid ")
	if !ok || !validInspectionPointerOID(oid) {
		return false
	}
	size, ok := strings.CutPrefix(lines[index+1], "size ")
	if !ok || size == "" {
		return false
	}
	for _, digit := range size {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	_, err := strconv.ParseInt(size, 10, 64)
	return err == nil
}

func validInspectionPointerOID(value string) bool {
	hash, ok := strings.CutPrefix(value, "sha256:")
	if !ok || len(hash) != 64 {
		return false
	}
	for _, digit := range hash {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') {
			return false
		}
	}
	return true
}
