// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

// visibleFiles 返回分组内通过过滤的文件（快照顺序）。结果来自缓存，调用方只读。
func (app *nativeApp) visibleFiles(staged bool) []gitrepo.File {
	return app.fileSection(staged).visible
}

func (app *nativeApp) checkedFiles(staged bool) []string {
	var files []string
	if app.repository == nil || len(app.selectedFiles) == 0 {
		return files
	}
	section := app.fileSection(staged)
	for index, file := range section.visible {
		if !file.Conflict && app.selectedFiles[section.keys[index]] {
			files = append(files, file.Path)
		}
	}
	return files
}

// hasCheckedFiles 与 checkedFiles 判断一致，但找到第一项即返回，供每遍构建使用。
func (app *nativeApp) hasCheckedFiles(staged bool) bool {
	if app.repository == nil || len(app.selectedFiles) == 0 {
		return false
	}
	section := app.fileSection(staged)
	for index, file := range section.visible {
		if !file.Conflict && app.selectedFiles[section.keys[index]] {
			return true
		}
	}
	return false
}

func (app *nativeApp) stageFiles(staged, all bool) {
	var files []string
	if all {
		for _, file := range app.visibleFiles(staged) {
			if !file.Conflict {
				files = append(files, file.Path)
			}
		}
	} else {
		files = app.checkedFiles(staged)
	}
	app.stagePaths(staged, files)
}

func (app *nativeApp) stageSelectedFile() {
	if !app.workingFileActions() || app.readTask != nil {
		return
	}
	app.stagePaths(app.staged, []string{app.selectedFile})
}

func (app *nativeApp) stagePaths(staged bool, files []string) {
	if len(files) == 0 || app.repository == nil || app.busy || !app.writable || app.dialog != nil {
		return
	}
	if !staged && app.gitFeatures(app.repository.snapshot.Path).checks {
		app.inspectStage(files)
		return
	}
	app.mutate(locales.UpdatingIndex(), func(ctx context.Context, path string) error {
		if staged {
			return app.service.Unstage(ctx, path, files)
		}
		return app.service.Stage(ctx, path, files)
	})
}

func (app *nativeApp) branchDialog() {
	if !app.canCreateReference() {
		return
	}
	app.referenceDialog(false, "HEAD · "+app.repository.snapshot.Branch, app.repository.snapshot.Head, true)
}

func (app *nativeApp) renameBranchDialog(old string) {
	app.dialog = &nativeDialog{
		title: locales.RenameBranch(), action: locales.Rename(), fields: []nativeField{{label: locales.BranchName(), value: old}},
		submit: func(dialog *nativeDialog) {
			name := dialog.fields[0].value
			app.mutateForm(dialog, locales.RenamingBranch(), func(ctx context.Context, path string) error {
				return app.service.RenameBranch(ctx, path, old, name)
			})
		},
	}
}

func (app *nativeApp) deleteBranch(name string, force bool) {
	if !app.canCreateReference() {
		return
	}
	path := app.repository.snapshot.Path
	app.load(locales.ReviewingWorktree(), nativeReadCommit, func(ctx context.Context) (func(), error) {
		review, err := app.service.ReviewBranchDeletion(ctx, path, name, force)
		if err != nil {
			return nil, err
		}
		return func() {
			if app.dialog != nil {
				return
			}
			dialog := &nativeDialog{
				title: locales.DeleteLocalBranch(), detail: locales.DeleteBranchDetail(),
				subject: name, danger: true, confirmation: true, width: 380,
			}
			action := locales.Delete()
			if force {
				dialog.title = locales.ForceDeleteLocalBranch()
			}
			if review.Worktree != nil {
				dialog.width = 600
				dialog.title = locales.RemoveWorktreeAndDeleteBranchTitle()
				if force {
					dialog.title = locales.RemoveWorktreeAndForceDeleteBranchTitle()
				}
				dialog.detail = review.Worktree.Path + "\n" + locales.RemoveWorktreeAndDeleteBranchDetail()
				action = locales.RemoveWorktreeAndDeleteBranch()
			}
			if force {
				dialog.detail += "\n" + locales.ForceDeleteBranchDetail()
			}
			if review.CanDelete {
				dialog.action = action
				dialog.submit = func(dialog *nativeDialog) {
					app.mutateForm(dialog, locales.DeletingBranch(), func(ctx context.Context, path string) error {
						return app.service.DeleteReviewedBranch(ctx, path, name, force, review.Token)
					})
				}
				dialog.onResult = func(err error) {
					var partial *gitrepo.BranchDeletionPartialError
					if errors.As(err, &partial) {
						dialog.blocked, dialog.action, dialog.submit = true, "", nil
						dialog.dismiss = locales.Close()
					}
				}
			} else {
				dialog.blocked, dialog.problem, dialog.dismiss = true, review.Problem, locales.Close()
			}
			app.dialog = dialog
		}, nil
	})
}

func (app *nativeApp) tagDialog() {
	if app.repository == nil || app.busy || !app.writable || app.readTask != nil {
		return
	}
	hash := app.commitHash
	if hash == "" {
		hash = app.repository.snapshot.Head
	}
	app.referenceDialog(true, locales.ReferenceCommit(), hash, false)
}

func (app *nativeApp) deleteTag(name string) {
	app.confirm(locales.DeleteLocalTag(), name, locales.Delete(), func() {
		app.mutate(locales.DeletingTag(), func(ctx context.Context, path string) error {
			return app.service.DeleteTag(ctx, path, name)
		})
	})
}

func (app *nativeApp) stashDialog() {
	app.dialog = &nativeDialog{
		title: locales.NewStash(), action: locales.Stash(), fields: []nativeField{{label: locales.OptionalDescription(), optional: true}},
		checkLabel: locales.IncludeUntracked(),
		canSubmit: func() bool {
			for _, file := range app.repository.snapshot.Files {
				if !file.Conflict && (file.Index != "?" || app.dialog != nil && app.dialog.check) {
					return true
				}
			}
			return false
		},
		submit: func(dialog *nativeDialog) {
			message, untracked := dialog.fields[0].value, dialog.check
			app.leaveConflict(func() {
				app.dialog = dialog
				app.mutateForm(dialog, locales.SavingStash(), func(ctx context.Context, path string) error {
					return app.service.CreateStash(ctx, path, message, untracked)
				})
			})
		},
	}
}

func (app *nativeApp) applyStash(hash string, pop bool) {
	if app.repository == nil || app.busy || !app.writable || app.readTask != nil || app.dialog != nil || hash == "" {
		return
	}
	title, detail, action := locales.ApplyStashTitle(), locales.ApplyStashDetail(app.repository.snapshot.Path), locales.Apply()
	if pop {
		title, detail, action = locales.PopStashTitle(), locales.PopStashDetail(app.repository.snapshot.Path), locales.ApplyAndDrop()
	}
	app.confirm(title, detail, action, func() {
		app.mutate(locales.ApplyingStash(), func(ctx context.Context, path string) error {
			return app.service.ApplyStash(ctx, path, hash, pop)
		})
	})
	app.dialog.danger = pop
}

func (app *nativeApp) dropStash(hash string) {
	if app.repository == nil || app.busy || !app.writable || app.readTask != nil || app.dialog != nil || hash == "" {
		return
	}
	app.confirm(locales.DeleteStashTitle(), shortHash(hash)+"\n"+locales.CannotUndo(), locales.Delete(), func() {
		app.mutate(locales.DeletingStash(), func(ctx context.Context, path string) error {
			return app.service.DropStash(ctx, path, hash)
		})
	})
}

func (app *nativeApp) discardFile() {
	path, file := app.repository.snapshot.Path, app.selectedFile
	app.run(locales.CheckingRecovery(), func(ctx context.Context) (func(), error) {
		preview, err := app.service.DiscardPreview(ctx, path, file)
		return func() {
			if err != nil {
				return
			}
			if !preview.Supported {
				app.problem = preview.Reason
				return
			}
			app.confirmTrashDiscard(file, preview)
		}, err
	})
}

func (app *nativeApp) resolveConflict(choice string) {
	conflict := *app.conflict
	content := ""
	if app.conflictEdit.Buffer != nil {
		content = app.conflictEdit.Buffer.String()
	}
	action := func() {
		app.mutate(locales.ResolvingConflict(), func(ctx context.Context, path string) error {
			return app.service.ResolveConflict(ctx, path, conflict.Path, conflict.Token, choice, content)
		})
	}
	if choice == "result" {
		action()
	} else {
		title, detail, label := locales.ResolveOursTitle(), locales.ResolveOursDetail(), locales.UseOurs()
		switch choice {
		case "theirs":
			title, detail, label = locales.ResolveTheirsTitle(), locales.ResolveTheirsDetail(), locales.UseTheirs()
		case "delete":
			title, detail, label = locales.ResolveDeleteTitle(), locales.ResolveDeleteDetail(), locales.DeleteFile()
		}
		app.confirm(title, conflict.Path+"\n"+detail, label, action)
	}
}

func (app *nativeApp) operationAction(action string) {
	operation := app.repository.snapshot.Operation
	fn := func() {
		app.leaveConflict(func() {
			app.mutate(locales.ProcessingOperation(operationName(operation.Kind)), func(ctx context.Context, path string) error {
				return app.service.OperationAction(ctx, path, operation.Kind, operation.Token, action)
			})
		})
	}
	if action == "continue" {
		fn()
	} else {
		title, detail, label := locales.AbortOperationTitle(operationName(operation.Kind)), locales.AbortOperationDetail(), locales.AbortOperation(operationName(operation.Kind))
		if action == "skip" {
			title, detail, label = locales.SkipCommitTitle(), locales.SkipCommitDetail(), locales.SkipCommit()
		}
		app.confirm(title, app.repository.snapshot.Path+"\n"+detail, label, fn)
	}
}

func (app *nativeApp) remoteAction(action string) {
	if len(app.repository.remotes) == 0 {
		app.editRemoteDialog(nil)
		app.dialog.detail = locales.NoRemoteAddress()
		return
	}
	if action == "pull" && app.repository.snapshot.Upstream == "" {
		app.dialog = &nativeDialog{
			title: locales.NoUpstreamTitle(), detail: locales.NoUpstreamDetail(),
			action: locales.PublishBranchMenu(), submit: func(_ *nativeDialog) { app.publishDialog() },
		}
		return
	}
	if action == "push" && app.repository.snapshot.Upstream == "" {
		app.publishDialog()
		return
	}
	if action == "push" && len(app.repository.remotes) > 1 {
		app.branchPushDialog(false)
		return
	}
	app.leaveConflict(func() {
		app.mutate(map[string]string{"fetch": locales.Fetching(), "pull": locales.Pulling(), "push": locales.Pushing()}[action], func(ctx context.Context, path string) error {
			return app.service.Remote(ctx, path, action)
		})
	})
}

func (app *nativeApp) remoteDialog() {
	app.dialog = &nativeDialog{
		title: locales.RemoteRepositories(),
		body:  app.remoteSettingsView,
	}
}

func (app *nativeApp) remoteSettingsView(c *ui.Context) {
	parent := app.dialog
	services := c.Services()
	if len(app.repository.remotes) == 0 {
		ui.Text(c, locales.NoRemotes()).TextColor(c.Theme().TextMuted)
	}
	ui.Column(c).FillWidth().Gap(0).Children(func() {
		if len(app.repository.remotes) > 0 {
			ui.Row(c).FillWidth().Gap(12).Padding(0, 0, 6).Children(func() {
				ui.Text(c, locales.RemoteName()).Width(96).Shrink(0).FontSize(11).TextColor(c.Theme().TextMuted)
				ui.Text(c, locales.RemoteAddress()).Grow(1).FontSize(11).TextColor(c.Theme().TextMuted)
			})
			ui.Divider(c)
		}
		for _, remote := range app.repository.remotes {
			remote := remote
			separatePush := len(remote.PushURLs) > 0 && !slices.Equal(remote.URLs, remote.PushURLs)
			ui.Row(c).Key(remote.Name).FillWidth().Gap(12).Padding(4, 0).AlignItems(ui.Center).Children(func() {
				ui.Text(c, remote.Name).Width(96).Shrink(0).FontSize(13).Bold().MaxLines(1).Tooltip(remote.Name)
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					for _, address := range remote.URLs {
						ui.Text(c, address).FontSize(12).MaxLines(1).Tooltip(address).Selectable()
					}
					if separatePush {
						for _, address := range remote.PushURLs {
							ui.Text(c, locales.Push()+": "+address).FontSize(12).MaxLines(1).Tooltip(address).Selectable()
						}
					}
				})
				iconButton(c, locales.Manage()+" "+remote.Name, nativeIcons["more"]).Shrink(0).
					Disabled(app.busy || app.closed).Menu(func(menu *ui.Menu) {
					unavailable := app.busy || app.closed || !app.writable
					if menu.Item(locales.EditRemoteMenu()).Disabled(unavailable).Chosen() {
						app.editRemoteDialog(&remote)
						app.dialog.parent = parent
					}
					if menu.Item(locales.CopyRemoteAddresses()).Disabled(len(remote.URLs) == 0).Chosen() {
						services.WriteClipboard(strings.Join(remote.URLs, "\n"))
					}
					if separatePush && menu.Item(locales.CopyRemotePushAddresses()).Chosen() {
						services.WriteClipboard(strings.Join(remote.PushURLs, "\n"))
					}
					menu.Separator()
					if menu.Item(locales.RemoveMenu()).Danger().Disabled(unavailable).Chosen() {
						app.dialog = &nativeDialog{
							parent: parent, title: locales.RemoveRemoteTitle(),
							detail: remote.Name + "\n" + locales.RemoveRemoteDetail(),
							action: locales.Remove(), danger: true,
							submit: func(dialog *nativeDialog) {
								app.mutateForm(dialog, locales.RemovingRemote(), func(ctx context.Context, path string) error {
									return app.service.RemoveRemote(ctx, path, remote.Name, remote.Token)
								})
							},
						}
					}
				})
			})
			ui.Divider(c)
		}
	})
	ui.Row(c).FillWidth().Children(func() {
		if ui.Button(c, locales.AddRemote()).Disabled(!app.writable || app.busy || app.closed).Clicked() {
			app.editRemoteDialog(nil)
			app.dialog.parent = parent
		}
	})
}

func (app *nativeApp) editRemoteDialog(remote *gitrepo.RemoteConfig) {
	name, address, token := "", "", ""
	if remote != nil {
		name, token = remote.Name, remote.Token
		if len(remote.URLs) > 0 {
			address = remote.URLs[0]
		}
	}
	app.dialog = &nativeDialog{
		title: locales.RemoteAddress(), action: locales.Save(),
		fields: []nativeField{{label: locales.RemoteName(), value: name, readOnly: remote != nil}, {label: locales.FetchAddress(), value: address}},
		submit: func(dialog *nativeDialog) {
			nextName, nextAddress := dialog.fields[0].value, dialog.fields[1].value
			if remote != nil && nextName != name {
				dialog.problem = locales.CannotRenameRemote()
				return
			}
			app.mutateForm(dialog, locales.SavingRemote(), func(ctx context.Context, path string) error {
				return app.service.SaveRemote(ctx, path, nextName, nextAddress, token)
			})
		},
	}
}

func (app *nativeApp) publishDialog() {
	app.branchPushDialog(true)
}

func (app *nativeApp) checkoutDialog(remote string) {
	_, name := remoteBranchParts(remote, app.repository.remotes)
	app.dialog = &nativeDialog{
		title: locales.CheckoutRemoteBranch(), detail: remote, action: locales.Checkout(),
		fields: []nativeField{{label: locales.LocalBranchName(), value: name}},
		submit: func(dialog *nativeDialog) {
			name := dialog.fields[0].value
			app.leaveConflict(func() {
				app.dialog = dialog
				app.mutateForm(dialog, locales.CheckingOutBranch(), func(ctx context.Context, path string) error {
					return app.service.CheckoutRemote(ctx, path, remote, name)
				})
			})
		},
	}
}

func operationName(kind string) string {
	switch kind {
	case "merge":
		return locales.Merge()
	case "rebase":
		return locales.Rebase()
	case "cherry-pick":
		return locales.CherryPick()
	case "revert":
		return locales.RevertCommit()
	default:
		return kind
	}
}

func (app *nativeApp) repositoryActions(menu *ui.Menu) {
	unavailable := app.busy || app.repository == nil
	if menu.Item(locales.NewTagMenu()).Disabled(unavailable || !app.writable || app.readTask != nil).Chosen() {
		app.tagDialog()
	}
	if menu.Item(locales.StashChangesMenu()).Disabled(unavailable || !app.writable ||
		len(app.repository.snapshot.Files) == 0 || app.repository.snapshot.Operation.Kind != "").Chosen() {
		app.stashDialog()
	}
	menu.Separator()
	if menu.Item(locales.OpenInTerminal()).Disabled(unavailable || app.closed || app.dialog != nil).Chosen() {
		app.openRepositoryTerminal(app.repository.snapshot.Path)
	}
	if menu.Item(revealRepositoryLabel()).Disabled(unavailable).Chosen() {
		if err := app.service.RevealRepository(app.repository.snapshot.Path); err != nil {
			app.problem = err.Error()
		}
	}
}
