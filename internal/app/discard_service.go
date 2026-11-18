// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"

	"github.com/egoist/mygo"
)

func (service *RepositoryService) DiscardPreview(ctx context.Context, path, file string) (gitrepo.DiscardPreview, error) {
	repo, err := service.manager.Get(path)
	if err != nil {
		return gitrepo.DiscardPreview{}, err
	}
	return repo.DiscardPreview(ctx, file)
}

func (service *RepositoryService) Discard(ctx context.Context, path, file, token string) (string, error) {
	repo, err := service.manager.Get(path)
	if err != nil {
		return "", err
	}
	directory, err := discardBackupDirectory()
	if err != nil {
		return "", err
	}
	return repo.Discard(ctx, file, token, directory)
}

func (service *RepositoryService) DiscardPermanently(ctx context.Context, path, file, token string) error {
	repo, err := service.manager.Get(path)
	if err != nil {
		return err
	}
	return repo.DiscardPermanently(ctx, file, token)
}

func (service *RepositoryService) DiscardBackups(ctx context.Context, days int) ([]gitrepo.DiscardBackup, error) {
	directory, err := discardBackupDirectory()
	if err != nil {
		return nil, err
	}
	var cutoff time.Time
	if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
	}
	return gitrepo.DiscardBackups(ctx, directory, cutoff)
}

func (service *RepositoryService) RestoreDiscardBackup(ctx context.Context, id string) (gitrepo.DiscardBackup, error) {
	directory, err := discardBackupDirectory()
	if err != nil {
		return gitrepo.DiscardBackup{}, err
	}
	item, content, err := gitrepo.ReadDiscardBackup(directory, id)
	if err != nil {
		return item, err
	}
	repo, err := service.manager.Get(item.Repository)
	if err != nil {
		repo, err = gitrepo.Open(ctx, item.Repository)
		if err != nil {
			return item, gitrepo.ErrDiscardRestoreConflict
		}
	}
	return item, repo.RestoreDiscardBackup(ctx, item, content)
}

func (service *RepositoryService) RevealDiscardBackups() error {
	directory, err := discardBackupDirectory()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	return mygo.Shell.OpenPath(directory)
}

func discardBackupDirectory() (string, error) {
	directory, err := mygo.App.Path(mygo.PathUserData)
	return filepath.Join(directory, "discard-backups"), err
}
