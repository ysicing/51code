// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func largeFileRisks(report gitrepo.FileInspectionReport, threshold int64, staged bool, acknowledged map[string]string) []gitrepo.FileInspection {
	var risks []gitrepo.FileInspection
	for _, file := range report.Files {
		if file.Size < threshold || file.Pointer || file.LFS && !staged {
			continue
		}
		if staged && file.Fingerprint != "" && acknowledged[file.Path] == file.Fingerprint {
			continue
		}
		risks = append(risks, file)
	}
	return risks
}

func (app *nativeApp) acknowledgeFiles(path string, report gitrepo.FileInspectionReport, risks []gitrepo.FileInspection) {
	if app.largeFileAcknowledged == nil {
		app.largeFileAcknowledged = map[string]map[string]string{}
	}
	if app.largeFileAcknowledged[path] == nil {
		app.largeFileAcknowledged[path] = map[string]string{}
	}
	for _, file := range report.Files {
		if slices.ContainsFunc(risks, func(risk gitrepo.FileInspection) bool { return risk.Path == file.Path }) {
			app.largeFileAcknowledged[path][file.Path] = file.Fingerprint
		}
	}
}

func (app *nativeApp) inspectStage(files []string) {
	path := app.repository.snapshot.Path
	features := app.gitFeatures(path)
	var report gitrepo.FileInspectionReport
	var risks []gitrepo.FileInspection
	app.mutateFormResult(nil, locales.CheckingLargeFiles(), func(ctx context.Context, path string) error {
		var err error
		report, err = app.service.InspectFiles(ctx, path, files)
		if err != nil {
			return err
		}
		risks = largeFileRisks(report, features.threshold, false, nil)
		if len(risks) > 0 {
			return nil
		}
		_, err = app.service.StageInspected(ctx, path, files, report)
		return err
	}, func(err error) {
		if err != nil {
			return
		}
		if len(risks) > 0 {
			app.largeFileReview(path, files, report, risks, features, "")
		}
	})
}

func (app *nativeApp) inspectCommit(path, message string) {
	features := app.gitFeatures(path)
	acknowledged := maps.Clone(app.largeFileAcknowledged[path])
	app.run(locales.CheckingLargeFiles(), func(ctx context.Context) (func(), error) {
		report, err := app.service.InspectStagedFiles(ctx, path)
		if err != nil {
			return nil, err
		}
		risks := largeFileRisks(report, features.threshold, true, acknowledged)
		if len(risks) > 0 {
			return func() { app.largeFileReview(path, nil, report, risks, features, message) }, nil
		}
		return app.commitResult(ctx, path, message, report.Token)
	})
}

func fileInspectionSize(size int64) string {
	if size >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(size)/(1<<30))
	}
	return fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
}

func (app *nativeApp) largeFileReview(path string, files []string, report gitrepo.FileInspectionReport, risks []gitrepo.FileInspection, features nativeGitFeatures, message string) {
	if app.repository == nil || app.repository.snapshot.Path != path {
		return
	}
	commit := message != ""
	selected := map[string]bool{}
	for _, file := range risks {
		selected[file.Path] = file.Binary && file.Path != ".gitattributes"
	}
	dialog := &nativeDialog{
		title: locales.StageReviewTitle(), action: locales.StageNormally(), confirmation: true, width: 640,
		detail: locales.LargeFileReviewHint(strconv.FormatInt(features.threshold>>20, 10)),
	}
	normal := func() {
		app.dialog = nil
		if commit {
			app.commitMessage(path, message, report.Token)
		} else {
			app.stageReviewed(path, files, report, risks, nil)
		}
	}
	dialog.submit = func(_ *nativeDialog) { normal() }
	if commit {
		dialog.title, dialog.action = locales.CommitReviewTitle(), locales.ContinueCommit()
	} else if features.lfs {
		dialog.secondaryAction, dialog.secondarySubmit = locales.StageNormally(), normal
		dialog.action = locales.StageWithLFS()
		dialog.canSubmit = func() bool {
			for _, checked := range selected {
				if checked {
					return true
				}
			}
			return false
		}
		dialog.submit = func(_ *nativeDialog) {
			var lfs []string
			for _, file := range risks {
				if selected[file.Path] {
					lfs = append(lfs, file.Path)
				}
			}
			app.dialog = nil
			app.stageReviewed(path, files, report, risks, lfs)
		}
	}
	dialog.body = func(c *ui.Context) {
		ui.Column(c).FillWidth().Gap(12).Children(func() {
			_, height := c.Size()
			ui.Scroll(c).FillWidth().MaxHeight(min(240, max(40, height-340))).Children(func() {
				ui.Column(c).FillWidth().Gap(1).Children(func() {
					for _, file := range risks {
						ui.Row(c).Key(file.Path).FillWidth().Gap(12).Padding(8, 0).AlignItems(ui.Center).Children(func() {
							if features.lfs && !commit && file.Path != ".gitattributes" {
								checked := selected[file.Path]
								ui.Checkbox(c, &checked, "").Label(locales.SelectLFSFile(file.Path)).OnClick(func() { selected[file.Path] = checked })
							}
							ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
								ui.Text(c, file.Path).FontSize(13).SingleLine().Tooltip(file.Path)
								label := locales.LargeTextFile()
								if file.Binary {
									label = locales.LargeBinaryFile()
								}
								ui.Text(c, label).FontSize(11).TextColor(c.Theme().TextMuted)
							})
							ui.Text(c, fileInspectionSize(file.Size)).FontSize(12).TextColor(c.Theme().TextMuted)
						})
						ui.Divider(c)
					}
				})
			})
			if commit {
				ui.Text(c, locales.CommitLFSHint()).FontSize(12).FillWidth().TextColor(c.Theme().TextMuted)
			} else {
				if count := len(files) - len(risks); count > 0 {
					ui.Text(c, locales.NormalFilesSummary(strconv.Itoa(count))).FontSize(12).TextColor(c.Theme().TextMuted)
				}
				if features.lfs {
					ui.Text(c, locales.LfsReviewDetail()).FontSize(12).FillWidth().TextColor(c.Theme().TextMuted)
				}
			}
		})
	}
	app.dialog = dialog
}

func (app *nativeApp) stageReviewed(path string, files []string, report gitrepo.FileInspectionReport, risks []gitrepo.FileInspection, lfs []string) {
	if app.repository == nil || app.repository.snapshot.Path != path || app.busy || !app.writable {
		return
	}
	ctx, cancel := context.WithCancel(app.ctx)
	app.operationCancel = cancel
	label := locales.UpdatingIndex()
	if len(lfs) > 0 {
		label = locales.StagingWithLFS()
	}
	var staged gitrepo.FileInspectionReport
	app.mutateFormResult(nil, label, func(_ context.Context, path string) error {
		defer cancel()
		var err error
		if len(lfs) == 0 {
			staged, err = app.service.StageInspected(ctx, path, files, report)
			return err
		}
		current, err := app.service.InspectFiles(ctx, path, files)
		if err != nil {
			return err
		}
		if !slices.Equal(current.Files, report.Files) || current.Token != report.Token {
			return errors.New(locales.FileChangedRecheck())
		}
		repo, err := app.service.manager.Get(path)
		if err != nil {
			return err
		}
		if err := initializeLFSIfNeeded(ctx, repo); err != nil {
			return err
		}
		if err := repo.StageWithLFSInspected(ctx, lfs, report); err != nil {
			return err
		}
		var ordinary []string
		for _, file := range files {
			// LFS 操作只暂存新增规则；不能随后将用户的属性草稿整份加入索引。
			if file != ".gitattributes" && !slices.Contains(lfs, file) {
				ordinary = append(ordinary, file)
			}
		}
		if len(ordinary) > 0 {
			remaining, err := app.service.InspectFiles(ctx, path, ordinary)
			if err != nil {
				return err
			}
			for _, file := range remaining.Files {
				original := slices.IndexFunc(report.Files, func(previous gitrepo.FileInspection) bool { return previous.Path == file.Path })
				if original < 0 || report.Files[original] != file {
					return errors.New(locales.FileChangedRecheck())
				}
			}
			staged, err = app.service.StageInspected(ctx, path, ordinary, remaining)
			if err != nil {
				return err
			}
		} else {
			staged, err = app.service.InspectStagedFiles(ctx, path)
		}
		return err
	}, func(err error) {
		app.operationCancel = nil
		if err == nil {
			app.acknowledgeFiles(path, staged, risks)
		}
	})
}
