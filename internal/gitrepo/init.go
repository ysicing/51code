// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const initialBranch = "main"

func InitCandidate(ctx context.Context, path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	directory, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", nil
	}
	if _, err := os.Lstat(filepath.Join(directory, ".git")); !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	_, err = run(ctx, directory, "", 15*time.Second, "rev-parse", "--absolute-git-dir")
	if exitCode(err, 128) && strings.HasPrefix(err.Error(), "fatal: not a git repository") {
		return directory, nil
	}
	return "", err
}

func Init(ctx context.Context, directory string) error {
	candidate, err := InitCandidate(ctx, directory)
	if err != nil {
		return err
	}
	if candidate != directory {
		return errors.New("所选目录已变化或已属于 Git 仓库，请重新添加")
	}
	_, err = run(ctx, directory, "", 30*time.Second, "init", "--template=", "--initial-branch="+initialBranch)
	return err
}

func WriteInitialGitignore(ctx context.Context, directory, template string) error {
	if strings.TrimSpace(template) == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	const name = ".gitignore"
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	_, writeErr := file.WriteString(template)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("%s: %w", name, errors.Join(err, root.Remove(name)))
	}
	return nil
}
