// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"
	"strconv"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func (app *nativeApp) securityPreferences(c *ui.Context) {
	settingsSection(c, "Gitleaks", func() {
		enabled := app.prefs.SecretScanEnabled
		ui.Checkbox(c, &enabled, locales.EnableSecretScan()).OnClick(func() {
			previous := *app.prefs
			app.prefs.SecretScanEnabled = enabled
			app.saveBehaviorPreferences(previous)
		})
		ui.Text(c, locales.SecretScanSettingDetail()).FontSize(12).TextColor(c.Theme().TextMuted).FillWidth()
	})
}

func (app *nativeApp) showsStagedChanges() bool {
	return app.repository != nil && (app.screen == "changes" || app.screen == "history" && app.historyWorking)
}

func (app *nativeApp) canScanStagedSecrets() bool {
	return app.showsStagedChanges() && !app.closed && !app.busy && app.readTask == nil &&
		app.dialog == nil && app.prefs.SecretScanEnabled && app.fileSection(true).total > 0
}

func (app *nativeApp) scanStagedSecrets() {
	if !app.canScanStagedSecrets() {
		return
	}
	path := app.repository.snapshot.Path
	app.load(locales.ScanningStagedSecrets(), nativeReadCommit, func(ctx context.Context) (func(), error) {
		scan, err := app.service.ScanStagedSecrets(ctx, path)
		if err != nil {
			return nil, err
		}
		return func() {
			if app.dialog == nil {
				app.secretScanDialog(path, scan, nil)
			}
		}, nil
	})
}

func (app *nativeApp) secretScanDialog(path string, scan gitrepo.SecretScan, proceed func()) {
	if !app.showsStagedChanges() || app.repository.snapshot.Path != path {
		return
	}
	dialog := &nativeDialog{title: locales.SecretScanTitle(), width: 560, dismiss: locales.Close()}
	dialog.detail = locales.SecretScanClean()
	if len(scan.Findings) > 0 {
		count := strconv.Itoa(len(scan.Findings))
		dialog.detail = locales.SecretScanFound(count)
		if scan.Limited {
			dialog.detail = locales.SecretScanLimited(count)
		}
		dialog.body = func(c *ui.Context) {
			_, height := c.Size()
			ui.Scroll(c).FillWidth().MaxHeight(min(280, max(56, height-290))).Children(func() {
				ui.Column(c).FillWidth().Gap(8).Children(func() {
					for _, finding := range scan.Findings {
						location := fmt.Sprintf("%s:%d", finding.Path, finding.Line)
						ui.Row(c).Key(finding).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Button(c, location).MinWidth(0).Grow(1).Tooltip(location).OnClick(func() {
								app.closeDialog(dialog)
								app.chooseChangeFile(gitrepo.File{Path: finding.Path}, true)
							})
							ui.Text(c, finding.RuleID).FontSize(12).MinWidth(0).Shrink(1).SingleLine().Tooltip(finding.RuleID)
						})
					}
				})
			})
		}
	}
	if proceed != nil {
		dialog.dismiss, dialog.action = locales.Cancel(), locales.ContinueCommit()
		dialog.danger, dialog.confirmation = true, true
		dialog.submit = func(_ *nativeDialog) {
			app.closeDialog(dialog)
			proceed()
		}
	}
	app.dialog = dialog
}
