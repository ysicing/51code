// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"runtime"
	"slices"
	"strconv"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type nativeLFSState struct {
	path   string
	status gitrepo.LFSStatus
}

func initializeLFSIfNeeded(ctx context.Context, repo *gitrepo.Repository) error {
	status, err := repo.LFSStatus(ctx)
	if err != nil {
		return err
	}
	if !status.Installed {
		return repo.InitializeLFS(ctx)
	}
	return nil
}

func (app *nativeApp) lfsExecutableOptions() gitrepo.ExecutableOptions {
	options := app.prefs.LFS
	options.Resources, _ = mygo.App.Path(mygo.PathResources)
	return options
}

func lfsSourceLabel(source string) string {
	if source == "custom" {
		return locales.CustomLFS()
	}
	if source == "bundled" || source == "" && runtime.GOOS == "windows" {
		return locales.BundledLFS()
	}
	return locales.SystemLFS()
}

func (app *nativeApp) selectLFSExecutable(source, path string, editor *nativeIdentityEditor, dialog *nativeDialog) {
	if app.busy || app.closed || app.gitToolsBusy() {
		return
	}
	options := app.lfsExecutableOptions()
	options.Source, options.Path = source, path
	app.runTask(locales.DetectingLFS(), true, func(ctx context.Context) (func(), error) {
		info, err := gitrepo.InspectLFSExecutable(ctx, options)
		if err != nil {
			return nil, err
		}
		return func() {
			if app.dialog != dialog {
				return
			}
			if app.gitToolsBusy() {
				dialog.problem = locales.OperationBusyDetail()
				return
			}
			previous := *app.prefs
			app.prefs.LFS = gitrepo.ExecutableOptions{Source: source, Path: path}
			app.saveBehaviorPreferences(previous)
			if app.settingsError != "" {
				dialog.problem = app.settingsError
				return
			}
			gitrepo.ConfigureLFSExecutable(options)
			editor.lfsInfo, editor.lfsError = info, ""
			app.lfsState = nil
		}, nil
	})
}

func (app *nativeApp) lfsExecutablePreferences(c *ui.Context, editor *nativeIdentityEditor, dialog *nativeDialog) {
	settingsSection(c, "Git LFS", func() {
		settingsRow(c, locales.LfsSource(), func() {
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
				ui.MenuButton(c, lfsSourceLabel(app.prefs.LFS.Source), func(menu *ui.Menu) {
					if runtime.GOOS == "windows" && menu.Item(locales.BundledLFS()).Chosen() {
						app.selectLFSExecutable("bundled", "", editor, dialog)
					}
					if menu.Item(locales.SystemLFS()).Chosen() {
						app.selectLFSExecutable("system", "", editor, dialog)
					}
					if menu.Item(locales.ChooseLFS()).Chosen() {
						paths, err := app.openDialog(mygo.OpenDialogOptions{Parent: app.win, Title: locales.ChooseLFS()})
						if err != nil {
							dialog.problem = err.Error()
						} else if len(paths) > 0 {
							app.selectLFSExecutable("custom", paths[0], editor, dialog)
						}
					}
				}).Label(locales.LfsSource()).Width(210).Justify(ui.SpaceBetween).Disabled(app.busy || app.gitToolsBusy())
				if ui.Button(c, locales.DetectGit()).Clicked() {
					options := app.lfsExecutableOptions()
					app.runTask(locales.DetectingLFS(), true, func(ctx context.Context) (func(), error) {
						info, err := gitrepo.InspectLFSExecutable(ctx, options)
						return func() {
							editor.lfsInfo, editor.lfsError = info, ""
							if err != nil {
								editor.lfsError = err.Error()
							}
						}, nil
					})
				}
			})
			if editor.lfsInfo.Version != "" {
				ui.Text(c, editor.lfsInfo.Version).FontSize(12)
				ui.Text(c, editor.lfsInfo.Path).FontSize(11).TextColor(c.Theme().TextMuted).FillWidth().SingleLine().Tooltip(editor.lfsInfo.Path).Selectable()
			}
			if editor.lfsError != "" {
				ui.Text(c, locales.LfsUnavailable()).FontSize(12).TextColor(c.Theme().TextMuted).Tooltip(editor.lfsError)
				if ui.Button(c, locales.InstallLFS()).Clicked() {
					app.lfsInstallDialog(dialog)
				}
			}
		})
	})
}

func lfsInstallCommand(platform string) (string, string) {
	switch platform {
	case "darwin":
		return "Homebrew", "brew install git-lfs"
	case "linux":
		return "Debian / Ubuntu", "sudo apt-get install git-lfs"
	default:
		return "", ""
	}
}

func (app *nativeApp) lfsInstallDialog(parent *nativeDialog) {
	dialog := &nativeDialog{
		parent: parent, title: locales.InstallLFSTitle(), width: 520,
		closeLabel: locales.Close(),
	}
	dialog.onClose = func() { app.dialog = parent }
	dialog.body = func(c *ui.Context) {
		services := c.Services()
		ui.Column(c).FillWidth().Gap(12).Children(func() {
			if manager, command := lfsInstallCommand(runtime.GOOS); command != "" {
				ui.Text(c, manager).FontSize(12).TextColor(c.Theme().TextMuted)
				ui.Text(c, command).FontSize(13).FillWidth().Selectable()
				if ui.Button(c, locales.CopyInstallCommand()).Clicked() {
					services.WriteClipboard(command)
				}
			} else {
				ui.Text(c, locales.LfsDownloadInstaller()).FontSize(13).FillWidth()
			}
			if ui.Button(c, locales.OpenLFSWebsite()).Clicked() {
				dialog.problem = ""
				c.OpenURLThen("https://git-lfs.com/", func(err error) {
					if err != nil && app.dialog == dialog {
						dialog.problem = locales.LfsWebsiteOpenFailed(err.Error())
					}
				})
			}
			ui.Text(c, locales.LfsInstallHint()).FontSize(12).TextColor(c.Theme().TextMuted).FillWidth()
		})
	}
	app.dialog = dialog
}

func (app *nativeApp) readLFSStatus(path string) {
	app.runTask(locales.ReadingLFSStatus(), true, func(ctx context.Context) (func(), error) {
		repo, err := app.service.manager.Get(path)
		if err != nil {
			return nil, err
		}
		status, err := repo.LFSStatus(ctx)
		return func() { app.lfsState = &nativeLFSState{path, status} }, err
	})
}

func (app *nativeApp) repositoryLFSView(c *ui.Context, path string, parent *nativeDialog) {
	settingsSection(c, "Git LFS", func() {
		if !app.gitFeatures(path).lfs {
			return
		}
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if ui.Button(c, locales.CheckLFSStatus()).Clicked() {
				app.readLFSStatus(path)
			}
			if ui.Button(c, locales.InitializeLFS()).Disabled(!app.writable).Clicked() {
				app.confirm(locales.InitializeLFS(), locales.InitializeLFSDetail(), locales.FeatureEnabled(), func() {
					app.dialog = parent
					app.runLFSOperation(parent, locales.InitializingLFS(), func(ctx context.Context, repo *gitrepo.Repository) error {
						return repo.InitializeLFS(ctx)
					})
				})
				app.dialog.parent = parent
			}
		})
		state := app.lfsState
		if state == nil || state.path != path {
			return
		}
		label := locales.LfsNotInitialized()
		if state.status.Installed {
			label = locales.LfsInitialized()
		}
		ui.Text(c, label).FontSize(12).TextColor(c.Theme().TextMuted)
		if state.status.Problem != "" {
			app.inlineProblem(c, state.status.Problem)
		}
		if state.status.HooksPath != "" {
			ui.Text(c, state.status.HooksPath).FontSize(11).TextColor(c.Theme().TextMuted).SingleLine().Tooltip(state.status.HooksPath)
		}
		if len(state.status.Files) == 0 {
			return
		}
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.LfsTrackedFiles(strconv.Itoa(len(state.status.Files)))).FontSize(12).Grow(1)
			missing := slices.ContainsFunc(state.status.Files, func(file gitrepo.LFSFile) bool { return !file.Downloaded })
			if ui.Button(c, locales.DownloadLFS()).Disabled(!app.writable || !missing).Clicked() {
				app.runLFSOperation(parent, locales.DownloadingLFS(), func(ctx context.Context, repo *gitrepo.Repository) error {
					return repo.PullLFS(ctx)
				})
			}
		})
		for _, file := range state.status.Files {
			ui.Row(c).Key(file.Path).FillWidth().Gap(12).Children(func() {
				ui.Text(c, file.Path).FontSize(12).Grow(1).MinWidth(0).SingleLine().Tooltip(file.Path)
				label := locales.LfsDownloaded()
				if !file.Downloaded {
					label = locales.LfsMissing()
				}
				ui.Text(c, label).FontSize(11).TextColor(c.Theme().TextMuted)
			})
		}
	})
}

func (app *nativeApp) runLFSOperation(dialog *nativeDialog, label string, operation func(context.Context, *gitrepo.Repository) error) {
	if app.busy || app.repository == nil || !app.writable || !app.gitFeatures(app.repository.snapshot.Path).lfs {
		return
	}
	ctx, cancel := context.WithCancel(app.ctx)
	app.operationCancel = cancel
	var state *nativeLFSState
	app.mutateFormResult(nil, label, func(_ context.Context, path string) error {
		defer cancel()
		repo, err := app.service.manager.Get(path)
		if err != nil {
			return err
		}
		if err := operation(ctx, repo); err != nil {
			return err
		}
		status, err := repo.LFSStatus(ctx)
		state = &nativeLFSState{path, status}
		return err
	}, func(err error) {
		app.operationCancel = nil
		if state != nil {
			app.lfsState = state
		}
		if err != nil && dialog != nil {
			dialog.problem = err.Error()
		}
	})
}

func (app *nativeApp) lfsFileMenu(c *ui.Context) {
	if app.repository == nil || !app.gitFeatures(app.repository.snapshot.Path).lfs {
		return
	}
	ui.MenuButton(c, "LFS", func(menu *ui.Menu) {
		for _, track := range []bool{true, false} {
			label, detail := locales.UntrackLFSFile(), locales.UntrackLFSDetail()
			if track {
				label, detail = locales.TrackLFSFile(), locales.TrackLFSDetail()
			}
			if menu.Item(label).Disabled(!app.writable || app.busy || app.selectedFile == "" || app.staged).Chosen() {
				file := app.selectedFile
				app.confirm(label, detail, locales.Apply(), func() {
					app.runLFSOperation(nil, locales.UpdatingLFS(), func(ctx context.Context, repo *gitrepo.Repository) error {
						if track {
							if err := initializeLFSIfNeeded(ctx, repo); err != nil {
								return err
							}
							return repo.TrackLFS(ctx, []string{file})
						}
						return repo.UntrackLFS(ctx, []string{file})
					})
				})
				app.dialog.subject = file
			}
		}
	}).Label(locales.LfsFileActions())
}
