// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"

	"github.com/ysicing/51code/internal/gitrepo"
)

func (service *RepositoryService) ScanStagedSecrets(ctx context.Context, path string) (gitrepo.SecretScan, error) {
	repo, err := service.manager.Get(path)
	if err != nil {
		return gitrepo.SecretScan{}, err
	}
	return repo.ScanStagedSecrets(ctx)
}

func (service *RepositoryService) InspectFiles(ctx context.Context, path string, files []string) (gitrepo.FileInspectionReport, error) {
	repo, err := service.manager.Get(path)
	if err != nil {
		return gitrepo.FileInspectionReport{}, err
	}
	return repo.InspectFiles(ctx, files)
}

func (service *RepositoryService) InspectStagedFiles(ctx context.Context, path string) (gitrepo.FileInspectionReport, error) {
	repo, err := service.manager.Get(path)
	if err != nil {
		return gitrepo.FileInspectionReport{}, err
	}
	return repo.InspectStagedFiles(ctx)
}

func (service *RepositoryService) StageInspected(ctx context.Context, path string, files []string, expected gitrepo.FileInspectionReport) (gitrepo.FileInspectionReport, error) {
	repo, err := service.manager.Get(path)
	if err != nil {
		return gitrepo.FileInspectionReport{}, err
	}
	return repo.StageInspected(ctx, files, expected)
}

func (service *RepositoryService) CommitInspected(ctx context.Context, path, message, token string) error {
	repo, err := service.manager.Get(path)
	if err != nil {
		return err
	}
	return repo.CommitInspected(ctx, message, token)
}
