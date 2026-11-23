// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

const defaultGitignoreTemplate = `.DS_Store
Thumbs.db
Desktop.ini
*.swp
*.swo
*.out
node_modules
.pnpm-store/
/graft/
.trail
/.trail/
.cache
.gradle
.npm
__pycache__
*.log
*.tmp
*.db
.history
.chat
.build
.task

_output
# bin
vendor
dist

*.diff
*.backup
*.bak
`

func (app *nativeApp) templatePreferences(c *ui.Context) {
	settingsSection(c, locales.GitignoreTemplate(), func() {
		ui.Button(c, locales.Edit()).OnClick(app.gitignoreTemplateDialog)
	})
}

func (app *nativeApp) gitignoreTemplateDialog() {
	draft := app.prefs.GitignoreTemplate
	parent := app.dialog
	dialog := &nativeDialog{
		parent: parent, title: locales.GitignoreTemplate(), detail: locales.GitignoreTemplateDetail(),
		action: locales.Save(), width: 500,
	}
	dialog.body = func(c *ui.Context) {
		ui.TextArea(c, &draft).Label(locales.GitignoreTemplate()).Lines(8, 10).FillWidth()
		ui.Button(c, locales.RestoreGitignoreTemplate()).Disabled(draft == defaultGitignoreTemplate).OnClick(func() {
			draft = defaultGitignoreTemplate
		})
	}
	dialog.canSubmit = func() bool { return draft != app.prefs.GitignoreTemplate }
	dialog.unsavedDetail = func() string {
		if dialog.canSubmit() {
			return locales.DiscardGitignoreTemplate()
		}
		return ""
	}
	dialog.onClose = func() {
		if detail := dialog.unsavedDetail(); detail != "" {
			app.confirm(locales.DiscardUnsavedTitle(), detail, locales.DiscardEdits(), func() { app.dialog = parent })
			app.dialog.parent = dialog
			return
		}
		app.dialog = parent
	}
	dialog.submit = func(_ *nativeDialog) {
		previous := app.prefs.GitignoreTemplate
		app.prefs.GitignoreTemplate = draft
		if err := app.savePreferences(); err != nil {
			app.prefs.GitignoreTemplate = previous
			dialog.problem = locales.SaveSettingsError(err.Error())
			return
		}
		app.dialog = parent
	}
	app.dialog = dialog
}
