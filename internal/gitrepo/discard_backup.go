// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"crypto/rand"
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
)

var ErrDiscardRestoreConflict = errors.New("恢复目标已变化")

type DiscardBackup struct {
	ID         string      `json:"-"`
	Repository string      `json:"repository"`
	Path       string      `json:"path"`
	Created    time.Time   `json:"created"`
	Present    bool        `json:"present"`
	Mode       os.FileMode `json:"mode"`
	Untracked  bool        `json:"untracked"`
	After      string      `json:"after,omitempty"`
	Version    int         `json:"version,omitempty"`
}

func discardFingerprint(content []byte, mode os.FileMode) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%o\x00", mode.Perm())
	hash.Write(content)
	return hex.EncodeToString(hash.Sum(nil))
}

func (item DiscardBackup) Restorable() bool {
	return item.Present || item.Version == 1 && item.After != ""
}

func readBackupFile(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("副本文件无效或超过大小限制")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("副本文件已替换")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if int64(len(data)) > limit {
		return nil, errors.New("副本文件超过大小限制")
	}
	return data, err
}

func openDiscardBackup(root *os.Root, id string) (*os.Root, DiscardBackup, error) {
	var item DiscardBackup
	if !strings.HasPrefix(id, "discard-") || filepath.Base(id) != id {
		return nil, item, errors.New("副本标识无效")
	}
	info, err := root.Lstat(id)
	if err != nil {
		return nil, item, err
	}
	if !info.IsDir() {
		return nil, item, errors.New("副本目录无效")
	}
	entry, err := root.OpenRoot(id)
	if err != nil {
		return nil, item, err
	}
	defer func() {
		if entry != nil {
			entry.Close()
		}
	}()
	opened, err := entry.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, item, errors.New("副本目录已替换")
	}
	data, err := readBackupFile(entry, "metadata.json", 128<<10)
	if err != nil {
		return nil, item, err
	}
	if err := json.Unmarshal(data, &item); err != nil {
		return nil, item, err
	}
	if !filepath.IsAbs(item.Repository) || item.Created.IsZero() || validPath(item.Path) != nil ||
		filepath.ToSlash(filepath.Clean(filepath.FromSlash(item.Path))) != item.Path ||
		item.Version < 0 || item.Version > 1 {
		return nil, item, errors.New("副本元数据无效")
	}
	item.ID = id
	result := entry
	entry = nil
	return result, item, nil
}

func DiscardBackups(ctx context.Context, directory, repository string, expireBefore time.Time) ([]DiscardBackup, error) {
	root, err := os.OpenRoot(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	folder, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer folder.Close()
	entries, err := folder.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var items []DiscardBackup
	var cleanupErr error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		backup, item, err := openDiscardBackup(root, entry.Name())
		if err != nil {
			continue
		}
		backup.Close()
		if repository != "" && item.Repository != repository {
			continue
		}
		if item.Version == 1 && !expireBefore.IsZero() && !item.Created.After(expireBefore) {
			if err := root.RemoveAll(item.ID); err == nil {
				continue
			} else if cleanupErr == nil {
				cleanupErr = err
			}
		}
		items = append(items, item)
	}
	slices.SortFunc(items, func(left, right DiscardBackup) int {
		if order := right.Created.Compare(left.Created); order != 0 {
			return order
		}
		return strings.Compare(left.ID, right.ID)
	})
	return items, cleanupErr
}

func ReadDiscardBackup(directory, id string) (DiscardBackup, []byte, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return DiscardBackup{}, nil, err
	}
	defer root.Close()
	entry, item, err := openDiscardBackup(root, id)
	if err != nil {
		return item, nil, err
	}
	defer entry.Close()
	if !item.Present {
		return item, nil, nil
	}
	content, err := readBackupFile(entry, "content", outputLimit)
	return item, content, err
}

func (repo *Repository) RestoreDiscardBackup(ctx context.Context, item DiscardBackup, content []byte) error {
	if err := validPath(item.Path); err != nil {
		return err
	}
	if item.Repository != repo.Path || !item.Restorable() || len(content) > outputLimit {
		return ErrDiscardRestoreConflict
	}
	if err := repo.lockHistory(ctx); err != nil {
		return err
	}
	defer repo.mu.Unlock()
	canonical, err := filepath.EvalSymlinks(repo.Path)
	if err != nil || canonical != repo.Path {
		return ErrDiscardRestoreConflict
	}
	actual, err := Open(ctx, repo.Path)
	if err != nil || actual.Path != repo.Path {
		return ErrDiscardRestoreConflict
	}
	root, err := os.OpenRoot(repo.Path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := repo.checkConflictAncestors(ctx, root, item.Path); err != nil {
		return err
	}
	unmerged, err := repo.git(ctx, "--literal-pathspecs", "ls-files", "-u", "--", item.Path)
	if err != nil {
		return err
	}
	if unmerged != "" {
		return ErrDiscardRestoreConflict
	}
	if !item.Present {
		if _, err := root.Lstat(item.Path); os.IsNotExist(err) {
			return nil
		}
	}
	directory := filepath.Dir(item.Path)
	if err := root.MkdirAll(directory, 0755); err != nil {
		return err
	}
	parent, err := root.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer parent.Close()
	name := filepath.Base(item.Path)
	temp := ".51code-restore-" + rand.Text()
	if item.Present {
		file, err := parent.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer parent.Remove(temp)
		writeErr := writeDiscardFile(file, content, item.Mode.Perm())
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return err
		}
	}
	if err := repo.checkConflictAncestors(ctx, root, item.Path); err != nil {
		return err
	}
	expected, err := root.Stat(directory)
	if err != nil {
		return err
	}
	opened, err := parent.Stat(".")
	if err != nil || !os.SameFile(expected, opened) {
		return ErrDiscardRestoreConflict
	}
	info, err := parent.Lstat(name)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if os.IsNotExist(err) {
		if item.Present {
			return parent.Link(temp, name)
		}
		return nil
	}
	if err != nil || item.Version != 1 || item.After == "" {
		return ErrDiscardRestoreConflict
	}
	current, err := readBackupFile(parent, name, outputLimit)
	latest, statErr := parent.Lstat(name)
	if err != nil || statErr != nil || !os.SameFile(info, latest) ||
		info.ModTime() != latest.ModTime() || info.Mode() != latest.Mode() ||
		discardFingerprint(current, latest.Mode()) != item.After {
		return ErrDiscardRestoreConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !item.Present {
		return parent.Remove(name)
	}
	return parent.Rename(temp, name)
}

func ExportDiscardBackup(target string, content []byte) error {
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	writeErr := writeDiscardFile(file, content, 0600)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return errors.Join(err, os.Remove(target))
	}
	return nil
}
