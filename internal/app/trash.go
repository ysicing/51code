// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type nativeTrashPreferences struct {
	Disabled   bool `json:"disabled,omitempty"`
	Configured bool `json:"configured,omitempty"`
	Days       int  `json:"days,omitempty"`
}

func (prefs nativeTrashPreferences) days() int {
	switch prefs.Days {
	case -1, 7, 90:
		return prefs.Days
	default:
		return 30
	}
}

func trashRetentionChoices() ([]int, []string) {
	return []int{7, 30, 90, -1}, []string{
		locales.TrashRetentionDays("7"), locales.TrashRetentionDays("30"),
		locales.TrashRetentionDays("90"), locales.TrashRetentionNever(),
	}
}

func (desktop *nativeDesktop) startTrashCleanup() {
	var timer *time.Timer
	stopped := false
	var schedule func()
	schedule = func() {
		if stopped {
			return
		}
		days := desktop.prefs.Trash.days()
		go func() {
			if _, err := desktop.service.DiscardBackups(context.Background(), days); err != nil {
				log.Printf("清理回收站：%v", err)
			}
		}()
		timer = time.AfterFunc(24*time.Hour, func() { mygo.RunOnMain(schedule) })
	}
	mygo.App.OnQuit(func() {
		stopped = true
		if timer != nil {
			timer.Stop()
		}
	})
	schedule()
}

func (app *nativeApp) storeTrashPreferences(prefs nativeTrashPreferences) error {
	previous := app.prefs.Trash
	app.prefs.Trash = prefs
	if err := app.savePreferences(); err != nil {
		app.prefs.Trash = previous
		return err
	}
	return nil
}

func (app *nativeApp) trashPreferences(c *ui.Context) {
	settingsSection(c, locales.Trash(), func() {
		c.Key("trash-enabled")
		enabled := !app.prefs.Trash.Disabled
		ui.Checkbox(c, &enabled, locales.TrashEnabled()).OnClick(func() {
			prefs := app.prefs.Trash
			prefs.Disabled, prefs.Configured = !enabled, true
			app.settingsError = ""
			if err := app.storeTrashPreferences(prefs); err != nil {
				app.settingsError = locales.SaveSettingsError(err.Error())
			}
		})
		settingsRow(c, locales.TrashRetention(), func() {
			days, labels := trashRetentionChoices()
			selected := labels[slices.Index(days, app.prefs.Trash.days())]
			c.Key("trash-retention")
			if ui.Select(c, &selected, labels).Label(locales.TrashRetention()).Width(210).Changed() {
				prefs := app.prefs.Trash
				prefs.Days = days[slices.Index(labels, selected)]
				app.settingsError = ""
				if err := app.storeTrashPreferences(prefs); err != nil {
					app.settingsError = locales.SaveSettingsError(err.Error())
				}
			}
		})
	})
}

func (app *nativeApp) discardFileLabel() string {
	for _, file := range app.repository.snapshot.Files {
		if file.Path == app.selectedFile && file.Index == "?" {
			if app.prefs.Trash.Disabled {
				return locales.TrashDeletePermanentlyMenu()
			}
			return locales.TrashMoveMenu()
		}
	}
	return locales.DiscardChangesMenu()
}

func (app *nativeApp) confirmTrashDiscard(file string, preview gitrepo.DiscardPreview) {
	if app.prefs.Trash.Disabled {
		title, action := locales.DiscardFileTitle(), locales.DiscardChanges()
		if preview.Untracked {
			title, action = locales.TrashDeletePermanentlyTitle(), locales.TrashDeletePermanently()
		}
		app.confirm(title, file+"\n"+locales.TrashPermanentDetail(), action, func() {
			if app.prefs.Trash.Disabled {
				app.executeTrashDiscard(file, preview, false)
			}
		})
		return
	}
	if app.prefs.Trash.Configured {
		app.executeTrashDiscard(file, preview, true)
		return
	}
	title, action := locales.TrashFirstChanges(), locales.DiscardChanges()
	if preview.Untracked {
		title, action = locales.TrashFirstDelete(), locales.TrashMove()
	}
	days, labels := trashRetentionChoices()
	dialog := &nativeDialog{
		title: title, subject: file, detail: locales.TrashFirstDetail(), action: action,
		confirmation: true, width: 420, choice: labels[slices.Index(days, app.prefs.Trash.days())],
	}
	dialog.body = func(c *ui.Context) {
		settingsRow(c, locales.TrashRetention(), func() {
			ui.Select(c, &dialog.choice, labels).Label(locales.TrashRetention()).Width(210)
		})
	}
	dialog.submit = func(dialog *nativeDialog) {
		prefs := app.prefs.Trash
		if prefs.Disabled {
			app.dialog = nil
			app.confirmTrashDiscard(file, preview)
			return
		}
		if !prefs.Configured {
			prefs.Configured, prefs.Days = true, days[slices.Index(labels, dialog.choice)]
			if err := app.storeTrashPreferences(prefs); err != nil {
				dialog.problem = locales.SaveSettingsError(err.Error())
				return
			}
		}
		app.dialog = nil
		app.executeTrashDiscard(file, preview, true)
	}
	app.dialog = dialog
}

type nativeTrashUndo struct {
	id         string
	deleted    bool
	hasContent bool
}

func (app *nativeApp) executeTrashDiscard(file string, preview gitrepo.DiscardPreview, keep bool) {
	label := locales.DiscardingWithBackup()
	if !keep {
		label = locales.TrashDiscarding()
	}
	app.trashUndo = nativeTrashUndo{}
	var backup string
	app.mutateFormResult(nil, label, func(ctx context.Context, path string) error {
		if !keep {
			return app.service.DiscardPermanently(ctx, path, file, preview.Token)
		}
		var err error
		backup, err = app.service.Discard(ctx, path, file, preview.Token)
		return err
	}, func(err error) {
		if err != nil || backup == "" {
			return
		}
		notice := nativeTrashUndo{id: filepath.Base(backup), deleted: preview.Untracked, hasContent: preview.Present}
		app.trashUndo = notice
		time.AfterFunc(10*time.Second, func() {
			app.post(func() {
				if !app.closed && app.trashUndo == notice {
					app.trashUndo = nativeTrashUndo{}
				}
			})
		})
	})
}

func (app *nativeApp) trashUndoNotice(c *ui.Context) {
	notice := app.trashUndo
	label := locales.TrashChangesSaved()
	if notice.deleted {
		label = locales.TrashFileSaved()
	}
	ui.Text(c, label).FontSize(12).MinWidth(0).MaxLines(1).Shrink(1)
	if ui.Button(c, locales.TrashUndo()).Disabled(app.dialog != nil).Clicked() {
		app.restoreTrash(notice.id, notice.hasContent, nil, nil)
	}
}

func (app *nativeApp) showTrash(selected string) {
	days := app.prefs.Trash.days()
	app.run(locales.TrashLoading(), func(ctx context.Context) (func(), error) {
		items, err := app.service.DiscardBackups(ctx, days)
		return func() {
			if err == nil {
				app.trashDialog(items, selected)
			}
		}, err
	})
}

func (app *nativeApp) trashDialog(items []gitrepo.DiscardBackup, selected string) {
	dialog := &nativeDialog{
		title: locales.Trash(), width: 670, height: 454, dismiss: locales.Close(),
	}
	query := ""
	restored := make(map[string]bool)
	dialog.body = func(c *ui.Context) {
		ui.Row(c).FillWidth().Gap(10).Children(func() {
			ui.TextInput(c, &query).Label(locales.TrashSearch()).Placeholder(locales.TrashSearch()).Grow(1).MinWidth(0).AutoFocus()
			ui.MenuButton(c, locales.More(), func(menu *ui.Menu) {
				if menu.Item(locales.OpenBackupFolder()).Chosen() {
					app.run(locales.TrashLoading(), func(context.Context) (func(), error) {
						return nil, app.service.RevealDiscardBackups()
					})
				}
			})
		})
		needle := strings.ToLower(strings.TrimSpace(query))
		ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
			found := false
			for _, item := range items {
				if !strings.Contains(strings.ToLower(item.Repository+" "+item.Path), needle) {
					continue
				}
				found = true
				c.Key(item.ID)
				row := ui.Row(c).FillWidth().Padding(7, 9).Gap(10).AlignItems(ui.Center)
				if selected == item.ID {
					row.Background(c.Theme().Accent.Alpha(0.12))
				}
				row.Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						title := filepath.Base(item.Path)
						if !item.Present {
							title = locales.TrashWasDeleted(title)
						}
						ui.Text(c, title).FontSize(13).Bold().MaxLines(1).Tooltip(item.Path)
						location := filepath.Base(item.Repository) + " · " + item.Path
						ui.Text(c, location).FontSize(11).TextColor(c.Theme().TextMuted).MaxLines(1).
							Tooltip(filepath.Join(item.Repository, filepath.FromSlash(item.Path)))
					})
					ui.Text(c, item.Created.Local().Format("2006-01-02 15:04")).FontSize(11).TextColor(c.Theme().TextMuted).Shrink(0)
					if restored[item.ID] {
						ui.Text(c, locales.TrashRestored()).FontSize(12).TextColor(c.Theme().TextMuted)
					} else {
						ui.Button(c, locales.TrashRestore()).Disabled(!item.Restorable()).OnClick(func() {
							app.restoreTrash(item.ID, item.Present, dialog, func() { restored[item.ID] = true })
						})
					}
				})
				ui.Divider(c)
			}
			if !found {
				label := locales.TrashEmpty()
				if needle != "" {
					label = locales.TrashNoMatches()
				}
				ui.Row(c).FillWidth().Padding(24).Justify(ui.Center).Children(func() {
					ui.Text(c, label).TextColor(c.Theme().TextMuted)
				})
			}
		})
	}
	app.dialog = dialog
}

func (app *nativeApp) restoreTrash(id string, hasContent bool, parent *nativeDialog, completed func()) {
	app.run(locales.TrashRestoring(), func(ctx context.Context) (func(), error) {
		item, err := app.service.RestoreDiscardBackup(ctx, id)
		if errors.Is(err, gitrepo.ErrDiscardRestoreConflict) {
			return func() { app.trashConflict(id, hasContent, parent) }, nil
		}
		return func() {
			if err != nil {
				return
			}
			if app.trashUndo.id == id {
				app.trashUndo = nativeTrashUndo{}
			}
			if completed != nil {
				completed()
			}
			if app.repository != nil && app.repository.snapshot.Path == item.Repository && !app.conflictDirty() {
				app.refresh()
			}
		}, err
	})
}

func (app *nativeApp) trashConflict(id string, hasContent bool, parent *nativeDialog) {
	dialog := &nativeDialog{
		title: locales.TrashConflictTitle(), detail: locales.TrashConflictDetail(), dismiss: locales.Close(),
		parent: parent, width: 415, confirmation: true,
	}
	if hasContent {
		dialog.secondaryAction = locales.TrashSaveAs()
		dialog.secondarySubmit = func() { app.exportTrash(id) }
	} else {
		dialog.detail = locales.TrashConflictWithoutContent()
	}
	app.dialog = dialog
}

func (app *nativeApp) exportTrash(id string) {
	win := app.win
	dialog := app.dialog
	app.run(locales.TrashLoading(), func(ctx context.Context) (func(), error) {
		directory, err := discardBackupDirectory()
		if err != nil {
			return nil, err
		}
		item, content, err := gitrepo.ReadDiscardBackup(directory, id)
		if err != nil || !item.Present {
			return nil, err
		}
		target, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent: win, Title: locales.TrashSaveAs(), ButtonLabel: locales.Save(),
			DefaultPath: filepath.Base(item.Path), CreateDirectories: true,
		})
		if err != nil || target == "" {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err = gitrepo.ExportDiscardBackup(target, content)
		return func() {
			if err == nil && app.dialog == dialog {
				app.closeDialog(dialog)
			}
		}, err
	})
}
