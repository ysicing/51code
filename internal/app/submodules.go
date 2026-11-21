// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

type nativeSubmodules struct {
	items []gitrepo.Submodule
	list  ui.ListState
}

func (app *nativeApp) readSubmodules(ctx context.Context, path string) ([]gitrepo.Submodule, error) {
	repo, err := app.service.manager.Get(path)
	if err != nil {
		return nil, err
	}
	return repo.Submodules(ctx)
}

func (app *nativeApp) loadSubmodules() {
	if app.repository == nil || app.screen != "submodules" || app.dialog != nil {
		return
	}
	path := app.repository.snapshot.Path
	app.load(locales.ReadingSubmodules(), nativeReadSubmodules, func(ctx context.Context) (func(), error) {
		items, err := app.readSubmodules(ctx, path)
		if err != nil {
			return nil, err
		}
		return func() { app.submodules.items = items }, nil
	})
}

func (app *nativeApp) canChangeSubmodules() bool {
	return !app.closed && (app.desktop == nil || !app.desktop.checkingQuit && !app.desktop.quitting) &&
		app.repository != nil && app.screen == "submodules" && app.writable && !app.busy &&
		app.readTask == nil && app.repository.snapshot.Operation.Kind == ""
}

func (app *nativeApp) runSubmoduleOperation(dialog *nativeDialog, label string, operation func(context.Context, *gitrepo.Repository) error) {
	if !app.canChangeSubmodules() || app.dialog != dialog {
		return
	}
	ctx, cancel := context.WithCancel(app.ctx)
	app.operationCancel = cancel
	app.mutateFormResult(dialog, label, func(_ context.Context, path string) error {
		defer cancel()
		repo, err := app.service.manager.Get(path)
		if err != nil {
			return err
		}
		return operation(ctx, repo)
	}, func(error) { app.operationCancel = nil })
}

func (app *nativeApp) addSubmoduleDialog() {
	if !app.canChangeSubmodules() || app.dialog != nil {
		return
	}
	app.dialog = &nativeDialog{
		title: locales.AddSubmodule(), action: locales.Add(), detail: locales.AddSubmoduleDetail(),
		fields: []nativeField{{label: locales.RemoteAddress()}, {label: locales.Path()}},
		submit: func(dialog *nativeDialog) {
			source, path := dialog.fields[0].value, dialog.fields[1].value
			app.runSubmoduleOperation(dialog, locales.AddingSubmodule(), func(ctx context.Context, repo *gitrepo.Repository) error {
				return repo.AddSubmodule(ctx, source, path)
			})
		},
	}
}

func (app *nativeApp) updateSubmoduleDialog(item gitrepo.Submodule) {
	if !app.canChangeSubmodules() || app.dialog != nil || !item.CanUpdate() {
		return
	}
	label := locales.UpdateSubmodule()
	if item.Head == "" {
		label = locales.InitializeSubmodule()
	}
	app.dialog = &nativeDialog{
		title: label, action: label, subject: item.Path, detail: locales.UpdateSubmoduleDetail(item.Target),
		confirmation: true,
		submit: func(dialog *nativeDialog) {
			app.runSubmoduleOperation(dialog, locales.UpdatingSubmodule(), func(ctx context.Context, repo *gitrepo.Repository) error {
				return repo.UpdateSubmodule(ctx, item)
			})
		},
	}
}

func (app *nativeApp) openSubmodule(item gitrepo.Submodule) {
	if app.repository == nil || app.screen != "submodules" || app.busy || app.dialog != nil ||
		app.readTask != nil || item.Head == "" {
		return
	}
	path := app.repository.snapshot.Path
	app.runTask(locales.OpeningRepository(), true, func(ctx context.Context) (func(), error) {
		repo, err := app.service.manager.Get(path)
		if err != nil {
			return nil, err
		}
		directory, err := repo.SubmodulePath(ctx, item.Path)
		if err != nil {
			return nil, err
		}
		return func() {
			if app.desktop != nil {
				app.desktop.openCanonicalRepository(app, directory)
			} else {
				app.openRepository(directory)
			}
		}, nil
	})
}

func (app *nativeApp) submoduleMenu(menu *ui.Menu, item gitrepo.Submodule) {
	if menu.Item(locales.Open()).Disabled(app.busy || app.readTask != nil || app.dialog != nil || item.Head == "").Chosen() {
		app.openSubmodule(item)
	}
	label := locales.UpdateSubmodule()
	if item.Head == "" {
		label = locales.InitializeSubmodule()
	}
	if menu.Item(label).Disabled(!app.canChangeSubmodules() || app.dialog != nil || !item.CanUpdate()).Chosen() {
		app.updateSubmoduleDialog(item)
	}
}

func submoduleStatus(item gitrepo.Submodule) string {
	switch {
	case item.Conflict:
		return locales.Conflicts()
	case item.Problem != "":
		return locales.SubmoduleUnavailable()
	case item.InProgress:
		return locales.SubmoduleInProgress()
	case item.Dirty:
		return locales.DirtyWorktree()
	case item.Head == "":
		return locales.SubmoduleUninitialized()
	case item.Head != item.Target:
		return locales.SubmoduleNeedsUpdate()
	default:
		return locales.SubmoduleUpToDate()
	}
}

func (app *nativeApp) submodulePanel(c *ui.Context) {
	state := &app.submodules
	ui.Column(c).Fill().MinWidth(0).Children(func() {
		ui.Row(c).FillWidth().Padding(10, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.Submodules()).Bold().Grow(1).MinWidth(0).SingleLine()
			ui.PrimaryButton(c, locales.AddSubmodule()).Disabled(!app.canChangeSubmodules() || app.dialog != nil).
				OnClick(app.addSubmoduleDialog)
		})
		ui.Divider(c)
		if len(state.items) == 0 {
			message := locales.NoSubmodules()
			if app.readTask != nil && app.readTask.kind == nativeReadSubmodules {
				message = locales.ReadingSubmodules()
			}
			if app.problem != "" && state.items == nil {
				return
			}
			ui.Box(c).Fill().Center().Children(func() {
				ui.Text(c, message).TextColor(c.Theme().TextMuted)
			})
			return
		}
		app.submoduleTable(c, state)
	})
}

func (app *nativeApp) submoduleTable(c *ui.Context, state *nativeSubmodules) {
	state.list.Key = func(index int) any { return state.items[index].Path }
	state.list.Label = func(index int) string { return state.items[index].Path }
	state.list.CellMenu = func(menu *ui.Menu, row, _ int) {
		if row >= 0 && row < len(state.items) {
			app.submoduleMenu(menu, state.items[row])
		}
	}
	columns := []ui.TableColumn{
		{Title: locales.Path(), MinWidth: 150},
		{Title: locales.Status(), MinWidth: 130},
		{Title: locales.SubmoduleCurrentCommit(), Width: 106, Fixed: true},
		{Title: locales.SubmoduleTargetCommit(), Width: 106, Fixed: true},
		{ID: "actions", Width: 46, Fixed: true},
	}
	ui.Table(c, &state.list, columns, len(state.items), func(index, column int) {
		item := state.items[index]
		switch column {
		case 0:
			ui.Text(c, item.Path).SingleLine().Tooltip(item.Path)
		case 1:
			status := submoduleStatus(item)
			detail := status
			if item.Problem != "" {
				detail = item.Problem
			}
			ui.Text(c, status).SingleLine().Tooltip(detail)
		case 2, 3:
			hash := item.Head
			if column == 3 {
				hash = item.Target
			}
			ui.Text(c, shortHash(hash)).Font("monospace").FontSize(12).SingleLine().Tooltip(hash)
		case 4:
			iconButton(c, locales.SubmoduleActions(), nativeIcons["more"]).Disabled(app.busy || app.dialog != nil).
				Menu(func(menu *ui.Menu) { app.submoduleMenu(menu, item) })
		}
	}).Label(locales.Submodules()).Fill()
}
