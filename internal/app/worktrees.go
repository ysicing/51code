// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

type nativeWorktrees struct {
	allItems       []gitrepo.Worktree
	items          []gitrepo.Worktree
	candidatesOnly bool
	selected       int
	list           *ui.ListState
	loaded         bool
	canceled       bool
}

func (state *nativeWorktrees) setItems(items []gitrepo.Worktree) {
	state.allItems, state.loaded, state.canceled = items, true, false
	state.filterItems()
}

func (state *nativeWorktrees) filterItems() {
	path := ""
	if state.selected >= 0 && state.selected < len(state.items) {
		path = state.items[state.selected].Path
	}
	var items []gitrepo.Worktree
	for _, item := range state.allItems {
		if !item.Main && (!state.candidatesOnly || item.CleanupCandidate) {
			items = append(items, item)
		}
	}
	state.items, state.selected = items, -1
	for index, item := range items {
		if item.Current && state.selected == -1 {
			state.selected = index
		}
		if path != "" && item.Path == path {
			state.selected = index
			break
		}
	}
	if state.selected == -1 && len(items) > 0 {
		state.selected = 0
	}
	if state.list == nil {
		state.list = &ui.ListState{}
	}
	state.list.Selected = &state.selected
	state.list.Choose(state.selected)
}

func (app *nativeApp) openWorktrees() {
	if app.repository == nil || app.busy || app.dialog != nil {
		return
	}
	app.leaveConflict(func() {
		app.switchWorkspace("worktrees")
		app.loadWorktrees()
	})
}

func (app *nativeApp) loadWorktrees() {
	if app.repository == nil || app.screen != "worktrees" || app.worktrees == nil || app.busy || app.dialog != nil {
		return
	}
	state, path := app.worktrees, app.repository.snapshot.Path
	state.canceled = false
	app.load(locales.ReadingWorktrees(), nativeReadCommit, func(ctx context.Context) (func(), error) {
		items, err := app.service.Worktrees(ctx, path)
		if err != nil {
			return nil, err
		}
		return func() { state.setItems(items) }, nil
	})
}

func (app *nativeApp) createWorktreeDialog() {
	if app.repository == nil || app.screen != "worktrees" || !app.writable || app.busy ||
		app.readTask != nil || app.dialog != nil {
		return
	}
	repository := app.repository.snapshot.Path
	app.dialog = &nativeDialog{
		title: locales.NewWorktreeTitle(), action: locales.Create(),
		fields: []nativeField{
			{
				label: locales.WorktreePath(),
				value: filepath.Join(filepath.Dir(repository), filepath.Base(repository)+"-worktree"),
			},
			{label: locales.BranchName()},
			{label: locales.WorktreeStart(), value: "HEAD", optional: true},
		},
		checkLabel: locales.ExistingWorktreeBranch(),
		submit: func(dialog *nativeDialog) {
			path := dialog.fields[0].value
			branch, start := strings.TrimSpace(dialog.fields[1].value), strings.TrimSpace(dialog.fields[2].value)
			existing := dialog.check
			app.mutateForm(dialog, locales.CreatingWorktree(), func(ctx context.Context, repository string) error {
				_, err := app.service.CreateWorktree(ctx, repository, path, branch, start, existing)
				return err
			})
		},
	}
}

func (app *nativeApp) reviewWorktree(path string, remove bool) {
	app.reviewWorktreeAction(path, remove, false)
}

func (app *nativeApp) reviewWorktreeCleanup(path string) {
	app.reviewWorktreeAction(path, true, true)
}

func (app *nativeApp) reviewWorktreeAction(path string, remove, cleanup bool) {
	if app.repository == nil || app.screen != "worktrees" || !app.writable || app.busy ||
		app.readTask != nil || app.dialog != nil {
		return
	}
	repository := app.repository.snapshot.Path
	app.load(locales.ReviewingWorktree(), nativeReadCommit, func(ctx context.Context) (func(), error) {
		reviewWorktree := app.service.ReviewWorktree
		if cleanup {
			reviewWorktree = app.service.ReviewWorktreeCleanup
		}
		review, err := reviewWorktree(ctx, repository, path)
		if err != nil {
			return nil, err
		}
		return func() {
			// 读取期间用户可能已打开设置等弹窗，不能直接替换掉其中的草稿。
			if app.dialog != nil {
				return
			}
			item := review.Worktree
			dialog := &nativeDialog{width: 660}
			if remove {
				dialog.title = locales.RemoveWorktreeTitle()
				dialog.detail = item.Path + "\n" + locales.WorktreeRemoveDetail()
				if item.Detached {
					dialog.detail = item.Path + "\n" + locales.RemoveDetachedWorktreeDetail() + "\n" + item.Head
				}
				if item.Branch != "" {
					dialog.detail += "\n" + locales.BranchName() + ": " + worktreeBranch(item)
				}
				if cleanup {
					dialog.title = locales.CleanupWorktreeTitle()
					if len(item.ContainedIn) > 0 {
						contained := locales.WorktreeCleanupDetail(worktreeContainedBranches(item))
						dialog.body = func(c *ui.Context) {
							ui.Scroll(c).FillWidth().MaxHeight(160).Children(func() {
								ui.Text(c, contained).FontSize(12).FillWidth().Selectable().Tooltip(contained)
							})
						}
					}
				}
				dialog.danger = true
				if review.CanRemove {
					dialog.action = locales.Remove()
					dialog.submit = func(dialog *nativeDialog) {
						app.mutateForm(dialog, locales.RemovingWorktree(), func(ctx context.Context, repository string) error {
							if cleanup {
								return app.service.RemoveWorktreeCleanup(ctx, repository, item.Path, review.Token)
							}
							return app.service.RemoveWorktree(ctx, repository, item.Path, review.Token)
						})
					}
				} else {
					dialog.blocked, dialog.problem = true, review.RemoveProblem
				}
			} else {
				dialog.title = locales.MergeIntoCurrent()
				dialog.detail = worktreeBranch(item) + " (" + shortHash(item.Head) + ") → " +
					strings.TrimPrefix(review.TargetBranch, "refs/heads/") + " (" + shortHash(review.TargetHead) + ")"
				dialog.body = func(c *ui.Context) {
					ui.Scroll(c).FillWidth().MaxHeight(200).Children(func() {
						ui.Column(c).FillWidth().MinWidth(0).Gap(8).Children(func() {
							ui.Text(c, item.Path).FillWidth().Selectable().Tooltip(item.Path)
							ui.Text(c, item.Head+" → "+review.TargetHead).FontSize(11).
								FillWidth().Selectable()
						})
					})
				}
				if review.CanMerge {
					dialog.action = locales.Merge()
					dialog.submit = func(dialog *nativeDialog) {
						app.mutateForm(dialog, locales.MergingWorktree(), func(ctx context.Context, repository string) error {
							return app.service.MergeWorktree(ctx, repository, item.Path, review.Token)
						})
					}
				} else {
					dialog.blocked, dialog.problem = true, review.MergeProblem
				}
			}
			if dialog.blocked {
				dialog.dismiss = locales.Close()
			}
			app.dialog = dialog
		}, nil
	})
}

func worktreeBranch(item gitrepo.Worktree) string {
	if item.Detached {
		return locales.DetachedWorktree()
	}
	if item.Bare {
		return locales.BareWorktree()
	}
	return strings.TrimPrefix(item.Branch, "refs/heads/")
}

func worktreeContainedBranches(item gitrepo.Worktree) string {
	names := make([]string, 0, len(item.ContainedIn))
	for _, branch := range item.ContainedIn {
		names = append(names, strings.TrimPrefix(branch, "refs/heads/"))
	}
	return strings.Join(names, ", ")
}

func (app *nativeApp) worktreeMenu(menu *ui.Menu, item gitrepo.Worktree, services ui.Services) {
	if menu.Item(locales.Open()).Disabled(app.busy || item.Current || item.Bare || item.Prunable).Chosen() {
		app.openRepository(item.Path)
	}
	if menu.Item(locales.CopyFullPath()).Chosen() {
		services.WriteClipboard(item.Path)
	}
	if menu.Item(locales.CopyWorktreeCommit()).Disabled(item.Head == "").Chosen() {
		services.WriteClipboard(item.Head)
	}
	menu.Separator()
	unavailable := app.repository == nil || app.busy || !app.writable || app.readTask != nil ||
		app.dialog != nil || item.Current || item.Bare
	if menu.Item(locales.MergeIntoCurrent()).Disabled(unavailable || item.Locked ||
		item.Prunable || item.Problem != "" || item.Operation != "" || item.Dirty || item.Head == "").Chosen() {
		app.reviewWorktree(item.Path, false)
	}
	if menu.Item(locales.Remove()).Danger().Disabled(unavailable || item.Main).Chosen() {
		app.reviewWorktree(item.Path, true)
	}
	if item.CleanupCandidate && menu.Item(locales.Cleanup()).Danger().Disabled(unavailable).Chosen() {
		app.reviewWorktreeCleanup(item.Path)
	}
}

func (app *nativeApp) worktreePanel(c *ui.Context) {
	if app.repository == nil || app.worktrees == nil {
		return
	}
	state := app.worktrees
	ui.Column(c).Fill().MinWidth(0).Children(func() {
		app.operationBar(c)
		ui.Row(c).FillWidth().Padding(10, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.Worktrees()).Bold().Grow(1).MinWidth(0).SingleLine()
			if app.readTask != nil {
				ui.Spinner(c).Size(14, 14)
				ui.Text(c, app.readTask.label).FontSize(11).MinWidth(0).Shrink(1).
					SingleLine().TextColor(c.Theme().TextMuted)
				ui.Button(c, locales.Cancel()).OnClick(func() {
					app.cancelRead()
					state.canceled = true
				})
			} else {
				iconButton(c, locales.Refresh(), nativeIcons["refresh"]).Disabled(app.busy).
					OnClick(app.loadWorktrees)
			}
			ui.PrimaryButton(c, locales.New()).
				Disabled(!app.writable || app.busy || app.readTask != nil || app.dialog != nil).
				OnClick(app.createWorktreeDialog)
		})
		ui.Row(c).FillWidth().Padding(0, 12, 10, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			filter := 0
			if state.candidatesOnly {
				filter = 1
			}
			ui.Segmented(c, &filter, locales.AttachedWorktrees(), locales.WorktreeCleanupCandidates()).
				Label(locales.Worktrees()).OnChange(func() {
				state.candidatesOnly = filter == 1
				state.filterItems()
			})
			for _, item := range state.allItems {
				if item.Main && !item.Current && !item.Bare && !item.Prunable {
					ui.Spacer(c)
					ui.Button(c, locales.OpenMainWorktree()).Disabled(app.busy).
						OnClick(func() { app.openRepository(item.Path) })
					break
				}
			}
		})
		ui.Divider(c)
		if state.canceled {
			ui.Text(c, locales.WorktreeReadCanceled()).FontSize(12).TextColor(c.Theme().TextMuted).
				FillWidth().Padding(8, 12)
		}
		if len(state.items) == 0 {
			ui.Column(c).Fill().Center().Padding(20).Gap(8).Children(func() {
				message := locales.WorktreesNotLoaded()
				switch {
				case app.readTask != nil:
					message = app.readTask.label
				case app.problem != "":
					app.inlineProblem(c, app.problem)
					return
				case state.canceled:
					return
				case state.loaded:
					message = locales.EmptyWorktrees()
					if state.candidatesOnly {
						message = locales.EmptyWorktreeCleanupCandidates()
					}
				}
				ui.Text(c, message).TextColor(c.Theme().TextMuted)
			})
			return
		}
		app.worktreeTable(c, state)
		if state.selected >= 0 && state.selected < len(state.items) {
			app.worktreeStatus(c, state.items[state.selected])
		}
	})
}

func (app *nativeApp) worktreeTable(c *ui.Context, state *nativeWorktrees) {
	services := c.Services()
	state.list.Key = func(index int) any { return state.items[index].Path }
	state.list.Label = func(index int) string { return state.items[index].Path }
	state.list.CellMenu = func(menu *ui.Menu, row, _ int) {
		if row >= 0 && row < len(state.items) {
			app.worktreeMenu(menu, state.items[row], services)
		}
	}
	theme := *c.Theme()
	theme.Spacing = 3.5
	theme.Accent = theme.Accent.Alpha(0.16)
	theme.InactiveAccent, theme.AccentText = theme.Accent, theme.Text
	columns := []ui.TableColumn{
		{Title: locales.WorktreeName(), MinWidth: 142},
		{Title: locales.Branch(), MinWidth: 76},
		{Title: locales.WorktreePath(), MinWidth: 100},
		{Title: locales.CommitID(), Width: 92, Fixed: true},
		{ID: "actions", Width: 134, Fixed: true},
	}
	ui.Themed(c, &theme).FillWidth().Grow(1).MinHeight(0).MinWidth(0).Children(func() {
		ui.Table(c, state.list, columns, len(state.items), func(index, column int) {
			item := state.items[index]
			selectableText := func(text ui.Element) {
				if text.SingleLine().Selectable().Focused() && index != state.selected {
					state.list.Choose(index)
				}
			}
			switch column {
			case 0:
				ui.Row(c).FillWidth().MinWidth(0).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, nativeIcons["folder"]).FontSize(14).Shrink(0)
					ui.Text(c, filepath.Base(item.Path)).Grow(1).MinWidth(0).SingleLine().Tooltip(item.Path)
					if item.Current {
						ui.Text(c, locales.CurrentWorktree()).FontSize(10).Shrink(0)
					} else if item.CleanupCandidate {
						ui.Text(c, locales.WorktreeCleanupCandidates()).FontSize(10).Shrink(0)
					}
				})
			case 1:
				branch := worktreeBranch(item)
				selectableText(ui.Text(c, branch).Tooltip(branch))
			case 2:
				selectableText(ui.Text(c, item.Path).Tooltip(item.Path))
			case 3:
				selectableText(ui.Text(c, shortHash(item.Head)).Font("monospace").FontSize(12).Tooltip(item.Head))
			case 4:
				if index != state.selected {
					return
				}
				ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Justify(ui.End).Children(func() {
					ui.Button(c, locales.Open()).Disabled(app.busy || item.Current || item.Bare || item.Prunable).
						OnClick(func() { app.openRepository(item.Path) })
					iconButton(c, locales.WorktreeActions(), nativeIcons["more"]).Shrink(0).Disabled(app.busy).Menu(func(menu *ui.Menu) {
						app.worktreeMenu(menu, item, services)
					})
				})
			}
		}).Label(locales.WorktreeList()).Fill()
	})
}

func (app *nativeApp) worktreeStatus(c *ui.Context, item gitrepo.Worktree) {
	var statuses []string
	if item.Locked {
		statuses = append(statuses, locales.LockedWorktree())
	}
	if item.Prunable {
		statuses = append(statuses, locales.PrunableWorktree())
	}
	if item.Dirty {
		statuses = append(statuses, locales.DirtyWorktree())
	}
	if item.Operation != "" {
		statuses = append(statuses, locales.OperationInProgress(operationName(item.Operation)))
	}
	for _, reason := range []string{item.LockReason, item.PruneReason} {
		if reason != "" {
			statuses = append(statuses, reason)
		}
	}
	if len(item.ContainedIn) > 0 {
		statuses = append(statuses, locales.WorktreeContainedIn(worktreeContainedBranches(item)))
	}
	if len(statuses) > 0 {
		status := strings.Join(statuses, " · ")
		ui.Divider(c)
		ui.Text(c, status).FillWidth().Padding(6, 12).FontSize(11).
			TextColor(c.Theme().TextMuted).SingleLine().Tooltip(status)
	}
	for _, problem := range []string{item.ContainmentProblem, item.Problem} {
		if problem != "" {
			app.inlineProblem(c, problem)
		}
	}
	if app.repository.snapshot.Operation.Kind == "merge" {
		ui.Row(c).FillWidth().Padding(6, 12).Children(func() {
			ui.Button(c, locales.FileChanges()).OnClick(func() {
				app.leaveConflict(func() { app.switchWorkspace("changes") })
			})
		})
	}
}
