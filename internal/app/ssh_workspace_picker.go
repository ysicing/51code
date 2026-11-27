// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"path"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/plugins/sfsymbols"
	"github.com/egoist/mygo/ui"
)

func (app *nativeApp) sshWorkspaceDialog() {
	if app.screen != "library" || app.busy || app.closed || app.dialog != nil ||
		app.desktop != nil && (app.desktop.checkingQuit || app.desktop.quitting) {
		return
	}
	if app.group == "" {
		app.remoteLibrary = true
	}
	dialog := &nativeDialog{
		title: locales.ConnectSshRepository(), width: 540, closeLabel: locales.Close(),
		onClose: func() { app.dialog = nil },
	}
	var query, previousQuery string
	var aliases []string
	type hostChoice struct {
		host   string
		manual bool
	}
	var choices []hostChoice
	selected, refresh, loading := 0, true, true
	list := ui.ListState{Selected: &selected}
	choose := func(index int) {
		if app.dialog != dialog || index < 0 || index >= len(choices) {
			return
		}
		host := choices[index].host
		app.closeDialog(dialog)
		app.sshRepositoryPicker(gitrepo.SSHWorkspace{Host: host})
	}
	dialog.body = func(c *ui.Context) {
		if refresh || query != previousQuery {
			refresh, previousQuery = false, query
			choices = choices[:0]
			input := strings.TrimSpace(query)
			filter := strings.ToLower(input)
			exact := false
			for _, alias := range aliases {
				if strings.EqualFold(alias, input) {
					choices = append(choices, hostChoice{host: alias})
					exact = true
					break
				}
			}
			for _, alias := range aliases {
				if strings.Contains(strings.ToLower(alias), filter) && !strings.EqualFold(alias, input) {
					choices = append(choices, hostChoice{host: alias})
				}
			}
			if !exact && gitrepo.ValidSSHHost(input) {
				choices = append(choices, hostChoice{host: input, manual: true})
			}
			list.Choose(0)
			list.ScrollTo(0, ui.Start)
		}
		move := func(delta int) {
			if len(choices) > 0 {
				index := max(0, min(selected+delta, len(choices)-1))
				list.Choose(index)
				list.ScrollIntoView(index)
			}
		}
		label := locales.SshHost()
		ui.TextInput(c, &query).Label(label).Placeholder(label).Search().AutoFocus().FillWidth().
			OnShortcut(0, ui.KeyDown, func() { move(1) }).
			OnShortcut(0, ui.KeyUp, func() { move(-1) }).
			OnSubmit(func() { choose(selected) })
		if len(choices) == 0 {
			if loading {
				ui.Spinner(c).Size(16, 16).Label(locales.SshHost())
			} else {
				ui.Text(c, locales.NoSshHosts()).TextColor(c.Theme().TextMuted).Padding(8, 2)
			}
			return
		}
		list.Key = func(index int) any { return choices[index].host }
		list.Label = func(index int) string {
			choice := choices[index]
			if choice.manual {
				return locales.UseSshHost(choice.host)
			}
			return choice.host
		}
		_, windowHeight := c.Size()
		sshPickerList(c, &list, len(choices), windowHeight-130, dialog.title, false, func(index int) sshPickerRow {
			if choices[index].manual {
				return sshPickerRow{title: list.Label(index), symbol: "plus", fallback: "plus"}
			}
			return sshPickerRow{title: list.Label(index), symbol: "server.rack", fallback: "cloud"}
		}, choose)
	}
	ctx, cancel := context.WithCancel(app.ctx)
	dialog.cancel = cancel
	app.dialog = dialog
	go func() {
		hosts := gitrepo.SSHHostAliases(ctx)
		app.post(func() {
			defer cancel()
			if app.closed || app.dialog != dialog || ctx.Err() != nil {
				return
			}
			dialog.cancel = nil
			aliases, refresh, loading = hosts, true, false
		})
	}()
}

func (app *nativeApp) sshRepositoryPicker(repo gitrepo.SSHWorkspace) {
	host, draft := repo.Host, repo.Path
	dialog := &nativeDialog{
		title: locales.ChooseSshRepository(), action: locales.Open(), width: 540,
		canSubmit: func() bool { return strings.TrimSpace(draft) != "" },
	}
	changeHost := func() {
		if app.dialog != dialog {
			return
		}
		app.closeDialog(dialog)
		app.sshWorkspaceDialog()
	}
	var recent []string
	for _, saved := range app.library.SSHRepositories {
		if saved.Host == host {
			recent = append(recent, saved.Path)
		}
	}
	var directory gitrepo.SSHDirectory
	selected := -1
	list := ui.ListState{Selected: &selected}
	browsing := false
	browse := func(target string) {
		if app.dialog != dialog || dialog.blocked || browsing {
			return
		}
		previousDraft := draft
		ctx, cancel := context.WithCancel(app.ctx)
		dialog.cancel, dialog.problem, browsing = cancel, "", true
		go func() {
			result, err := gitrepo.ReadSSHDirectory(ctx, host, target)
			app.post(func() {
				defer cancel()
				if app.closed || app.dialog != dialog || ctx.Err() != nil {
					return
				}
				dialog.cancel, browsing = nil, false
				if err != nil {
					dialog.problem = err.Error()
					return
				}
				if draft == previousDraft {
					draft = result.Path
				}
				directory = result
				selected = -1
				list = ui.ListState{Selected: &selected}
			})
		}()
	}
	dialog.submit = func(*nativeDialog) {
		browsing = false
		app.openSSHRepository(dialog, gitrepo.SSHWorkspace{Host: host, Path: draft})
	}
	dialog.onChange = func() {
		if dialog.blocked && dialog.cancel != nil {
			dialog.cancel()
			dialog.cancel, dialog.blocked = nil, false
		}
		dialog.problem = ""
	}
	dialog.body = func(c *ui.Context) {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				sshPickerIcon(c, "server.rack", "cloud", 15)
				ui.Text(c, host).FontSize(13).Bold().SingleLine().Grow(1).MinWidth(0).Tooltip(host)
				ui.Button(c, locales.ChangeSshHost()).Shrink(0).OnClick(changeHost)
			})
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				parentLabel := locales.SshParentDirectory()
				ui.Button(c, "").Label(parentLabel).Tooltip(parentLabel).Size(30, 28).Padding(0).Shrink(0).Center().
					Disabled(directory.Path == "" || directory.Path == "/" || browsing || dialog.blocked).
					OnClick(func() { browse(path.Dir(directory.Path)) }).Children(func() {
					ui.Icon(c, nativeIcons["back"]).FontSize(15).Rotate(90).Role(ui.RoleNone)
				})
				label := locales.SshWorkspacePath()
				ui.TextInput(c, &draft).Label(label).Placeholder(label).Tooltip(draft).AutoFocus().Grow(1).MinWidth(0).
					OnChange(dialog.onChange).OnSubmit(func() {
					if app.dialog == dialog && !app.busy && dialog.valid() {
						dialog.submit(dialog)
					}
				})
				toolButton(c, locales.BrowseSshDirectory(), nativeIcons["folder"]).Shrink(0).
					Disabled(browsing || dialog.blocked).OnClick(func() { browse(draft) })
			})
			if directory.Path != "" && directory.Path != draft || dialog.blocked || browsing {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					if directory.Path != "" && directory.Path != draft {
						ui.Text(c, directory.Path).FontSize(12).TextColor(c.Theme().TextMuted).
							SingleLine().Grow(1).MinWidth(0).Tooltip(directory.Path)
					}
					if dialog.blocked || browsing {
						label := locales.BrowseSshDirectory()
						if dialog.blocked {
							label = locales.OpeningRepository()
						}
						ui.Spinner(c).Size(16, 16).Label(label)
					}
				})
			}
			entries, label := recent, locales.RecentSshRepositories()
			row := func(index int) sshPickerRow {
				entry := entries[index]
				return sshPickerRow{title: path.Base(entry), detail: entry, symbol: "point.3.connected.trianglepath.dotted", fallback: "branch"}
			}
			choose := func(index int) {
				if app.dialog == dialog && !dialog.blocked {
					draft = entries[index]
					dialog.submit(dialog)
				}
			}
			disabled := dialog.blocked
			if directory.Path != "" {
				entries, label = directory.Children, locales.BrowseSshDirectory()
				row = func(index int) sshPickerRow {
					return sshPickerRow{title: path.Base(entries[index]), symbol: "folder", fallback: "folder", navigates: true}
				}
				choose = func(index int) { browse(entries[index]) }
				disabled = disabled || browsing
				if len(entries) == 0 {
					ui.Text(c, locales.NoSshDirectories()).TextColor(c.Theme().TextMuted).Padding(8, 2)
				}
			}
			if len(entries) > 0 {
				list.Key = func(index int) any { return entries[index] }
				list.Label = func(index int) string {
					entry := row(index)
					if entry.detail != "" {
						return entry.detail
					}
					return entry.title
				}
				_, windowHeight := c.Size()
				room := windowHeight - 230
				if dialog.problem != "" {
					room -= 64
				}
				ui.Divider(c)
				sshPickerList(c, &list, len(entries), room, label, disabled, row, choose)
			}
		})
	}
	app.dialog = dialog
	switch {
	case repo.Path != "":
		dialog.submit(dialog)
	case len(recent) == 0:
		browse("")
	}
}

type sshPickerRow struct {
	title, detail    string
	symbol, fallback string
	navigates        bool
}

func sshPickerIcon(c *ui.Context, symbol, fallback string, size float32) {
	sfsymbols.Symbol(c, symbol).Size(size).Weight(sfsymbols.Medium).
		Color(c.Theme().TextMuted).Fallback(nativeIcons[fallback]).Shrink(0).Role(ui.RoleNone)
}

func sshPickerList(c *ui.Context, state *ui.ListState, count int, room float32, label string, disabled bool, item func(int) sshPickerRow, choose func(int)) {
	const rowHeight = 32
	height := min(float32(count)*rowHeight, max(rowHeight, min(8*rowHeight, room)))
	theme := *c.Theme()
	theme.Accent = theme.Accent.Alpha(0.16)
	theme.InactiveAccent, theme.AccentText = theme.Accent, theme.Text
	ui.Themed(c, &theme).FillWidth().Height(height).Children(func() {
		ui.List(c, state, count, func(index int) {
			entry := item(index)
			tooltip := entry.title
			if entry.detail != "" {
				tooltip = entry.detail
			}
			row := ui.Row(c).FillWidth().Height(rowHeight).Padding(0, 9).Gap(8).AlignItems(ui.Center).Tooltip(tooltip)
			row.Children(func() {
				sshPickerIcon(c, entry.symbol, entry.fallback, 15)
				title := ui.Text(c, entry.title).FontSize(13).SingleLine().MinWidth(0)
				if entry.detail != "" {
					title.MaxWidth(180)
					ui.Text(c, entry.detail).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine().Grow(1).MinWidth(0)
				} else {
					title.Grow(1)
				}
				if entry.navigates {
					sshPickerIcon(c, "chevron.right", "chevron", 11)
				}
			})
			if row.RowClicked() {
				choose(index)
			}
		}).FillWidth().Grow(1).MinHeight(0).Label(label).Disabled(disabled).OnSubmit(func() {
			if state.Selected != nil && *state.Selected >= 0 && *state.Selected < count {
				choose(*state.Selected)
			}
		})
	})
}
