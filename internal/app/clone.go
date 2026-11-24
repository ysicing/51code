// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func (app *nativeApp) cloneDialog() {
	if app.screen != "library" || app.busy || app.closed || app.dialog != nil {
		return
	}
	dialog := &nativeDialog{
		title: locales.CloneRepository(), action: locales.Clone(), width: 500,
		fields: []nativeField{
			{label: locales.RemoteAddress()},
			{label: locales.CloneDirectoryName()},
			{label: locales.CloneLocation(), value: app.directoryDialogPath(), readOnly: true},
		},
	}
	suggestedName := ""
	nameEdited := false
	dialog.onChange = func() {
		name := &dialog.fields[1].value
		nameEdited = nameEdited || *name != suggestedName
		if !nameEdited {
			suggestedName = gitrepo.SuggestCloneDirectoryName(dialog.fields[0].value)
			*name = suggestedName
		}
	}
	dialog.body = func(c *ui.Context) {
		ui.Button(c, locales.ChooseCloneLocation()).OnClick(func() {
			paths, err := app.chooseDirectories(locales.ChooseCloneLocation(), locales.CloneSelect(), false)
			if err != nil {
				dialog.problem = err.Error()
				return
			}
			if len(paths) != 0 {
				dialog.fields[2].value = paths[0]
				dialog.problem = ""
			}
		})
	}
	group := app.addRepositoryGroup()
	dialog.submit = func(dialog *nativeDialog) {
		source, name, parent := dialog.fields[0].value, dialog.fields[1].value, dialog.fields[2].value
		app.runClone(dialog, strings.TrimSpace(source), parent, name, group)
	}
	app.dialog = dialog
}

func (app *nativeApp) runClone(dialog *nativeDialog, source, parent, name, group string) {
	if app.desktop != nil && (app.desktop.checkingQuit || app.desktop.quitting) {
		return
	}
	ctx, cancel := context.WithCancel(app.ctx)
	state := &nativeRemoteProgress{progress: gitrepo.RemoteProgress{Percent: gitrepo.RemotePercentUnknown}}
	report := app.remoteProgressReporter(state)
	app.run(locales.CloningRepository(), func(appCtx context.Context) (func(), error) {
		defer cancel()
		repo, err := gitrepo.Clone(gitrepo.WithRemoteProgress(ctx, report), source, parent, name)
		if err != nil {
			return func() {
				app.operationCancel, app.remoteProgress = nil, nil
				if errors.Is(err, context.Canceled) {
					dialog.problem = locales.CloneCanceled()
				}
			}, err
		}
		path := repo.Path
		report(gitrepo.RemoteProgress{Phase: gitrepo.RemoteRefreshing, Percent: gitrepo.RemotePercentUnknown})
		library, err := app.service.AddRepositories(appCtx, []string{path}, group)
		return func() {
			app.operationCancel, app.remoteProgress = nil, nil
			if err != nil {
				dialog.title, dialog.action = locales.AddLocalRepository(), locales.Add()
				dialog.fields, dialog.body = nil, nil
				dialog.subject = path
				dialog.submit = func(_ *nativeDialog) { app.addClonedRepository(path, group) }
				return
			}
			app.library, app.dialog = library, nil
			app.openRepository(path)
		}, err
	})
	app.operationCancel, app.remoteProgress = cancel, state
}

func (app *nativeApp) addClonedRepository(path, group string) {
	app.run(locales.AddingRepositories(), func(ctx context.Context) (func(), error) {
		library, err := app.service.AddRepositories(ctx, []string{path}, group)
		if err != nil {
			return nil, err
		}
		return func() {
			app.library, app.dialog = library, nil
			app.openRepository(path)
		}, nil
	})
}
