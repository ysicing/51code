// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"strconv"
	"strings"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func (app *nativeApp) reviewCodeDiffView(c *ui.Context, index int, fit bool) ui.Element {
	state, unified := app.diffViews[index], &app.unifiedDiffs[index]
	if state.Layout == editor.DiffSplit {
		state.LineSelection = true
		state.RenderGutterButton = func(c *ui.Context, button ui.Element, line editor.DiffLineRange) {
			editor.DefaultGutterButton(c, button, line)
			button.Label(locales.AddReviewNote()).Tooltip(locales.AddReviewNote())
		}
		state.OnGutterButton = func(line editor.DiffLineRange) {
			if selected, ok := state.SelectedLines(); ok && selected.Side == line.Side &&
				selected.Start <= line.Start && line.End <= selected.End {
				line = selected
			}
			app.addReviewNote(unified.splitReviewSelection(state, line))
		}
		return editor.DiffView(c, state)
	}
	name := app.diffNames[index]
	if unified.state.Buffer == nil {
		unified.build(name)
	}
	unified.state.ContextMenu = func(menu *ui.Menu) {
		app.addReviewNoteMenu(menu, unified.reviewSelection(true))
	}
	return unified.view(c, name, state.Wrap, fit)
}

func (app *nativeApp) canReviewDiffSelection() bool {
	return !app.fileViewer.full && len(app.diffViews) == 1 && (!app.diff.Generated || app.fileViewer.generatedExpanded)
}

func (app *nativeApp) reviewNotesControl(c *ui.Context) {
	services := c.Services()
	iconButton(c, locales.ReviewNotes(), nativeIcons["feedback"]).Shrink(0).
		Disabled(app.reviewRepositoryKey() == "" || app.busy || app.dialog != nil).
		Menu(func(menu *ui.Menu) {
			repo := app.reviewRepositoryKey()
			if repo == "" {
				return
			}
			count := len(app.reviewNotes[repo])
			if app.canReviewDiffSelection() {
				state, unified := app.diffViews[0], &app.unifiedDiffs[0]
				var excerpts []reviewExcerpt
				if state.Layout == editor.DiffSplit {
					if selected, ok := state.SelectedLines(); ok {
						excerpts = unified.splitReviewSelection(state, selected)
					}
				} else {
					excerpts = unified.reviewSelection(false)
				}
				app.addReviewNoteMenu(menu, excerpts)
				if count > 0 {
					menu.Separator()
				}
			}
			if count == 0 {
				return
			}
			if menu.Item(locales.CopyReviewNotesMarkdown()).Trailing(strconv.Itoa(count)).Chosen() {
				services.WriteClipboard(app.reviewNotes.markdown(repo))
			}
			if menu.Item(locales.ClearReviewNotes()).Danger().Chosen() {
				app.confirm(locales.ClearReviewNotes(), locales.ClearReviewNotesConfirm(), locales.ClearReviewNotes(), func() {
					delete(app.reviewNotes, repo)
				})
			}
		})
}

func (app *nativeApp) addReviewNoteMenu(menu *ui.Menu, excerpts []reviewExcerpt) {
	if menu.Item(locales.AddReviewNote()).Disabled(len(excerpts) == 0 || app.busy || app.readTask != nil).Chosen() {
		app.addReviewNote(excerpts)
	}
}

func (app *nativeApp) addReviewNote(excerpts []reviewExcerpt) {
	repo := app.reviewRepositoryKey()
	if repo == "" || app.busy || app.readTask != nil || app.dialog != nil || len(excerpts) == 0 {
		return
	}
	if len(app.diffViews) == 1 {
		for index := range excerpts {
			source := app.diff.New
			if excerpts[index].side == editor.DiffOld {
				source = app.diff.Old
			}
			if source != nil && source.Path != "" {
				excerpts[index].path = source.Path
			}
		}
	}
	var locations []string
	for _, excerpt := range excerpts {
		locations = append(locations, excerpt.location())
	}
	var draft string
	dialog := &nativeDialog{
		title: locales.AddReviewNote(), subject: strings.Join(locations, "\n"), action: locales.Save(),
		closeLabel: locales.Close(), width: 480,
		body: func(c *ui.Context) {
			ui.TextArea(c, &draft).Label(locales.ReviewNote()).AutoFocus().Lines(3, 8).FillWidth()
		},
		canSubmit: func() bool { return strings.TrimSpace(draft) != "" },
		unsavedDetail: func() string {
			if strings.TrimSpace(draft) != "" {
				return locales.DiscardReviewNoteDraft()
			}
			return ""
		},
	}
	dialog.onClose = func() {
		if detail := dialog.unsavedDetail(); detail != "" {
			app.confirm(locales.DiscardUnsavedTitle(), detail, locales.DiscardEdits(), func() {})
			app.dialog.parent = dialog
			return
		}
		app.dialog = nil
	}
	dialog.submit = func(_ *nativeDialog) {
		if app.reviewNotes == nil {
			app.reviewNotes = make(nativeReviewNotes)
		}
		app.reviewNotes[repo] = append(app.reviewNotes[repo], reviewNote{excerpts, strings.TrimSpace(draft)})
		app.dialog = nil
	}
	app.dialog = dialog
}
