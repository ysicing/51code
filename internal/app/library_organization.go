// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/internal/workspace"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func (service *RepositoryService) MarkRepositoryOpened(ctx context.Context, path string) (workspace.Library, error) {
	return service.manager.MarkOpened(ctx, path)
}

func (service *RepositoryService) RelocateRepository(ctx context.Context, expected workspace.LocalRepository, path string) (workspace.Library, error) {
	return service.manager.Relocate(ctx, expected, path)
}

func (app *nativeApp) repositoryOpenFailure(path string, err error) (func(), error) {
	missing := errors.Is(err, gitrepo.ErrWorktreeAbsent)
	if !missing && !errors.Is(err, gitrepo.ErrNotWorktree) {
		return nil, err
	}
	return func() {
		if app.screen != "library" || app.dialog != nil ||
			!slices.ContainsFunc(app.service.Library().Repositories, func(entry workspace.LocalRepository) bool { return entry.Path == path }) {
			app.problem = err.Error()
			app.showProblemDetails(app.problem)
			return
		}
		title := locales.RepositoryNotWorktree()
		if missing {
			title = locales.RepositoryDirectoryMissing()
		}
		app.dialog = &nativeDialog{
			title: title, subject: path, detail: locales.KeepDiskFiles(),
			action: locales.RemoveFromListMenu(), dismiss: locales.Close(), confirmation: true, danger: true, width: 480,
			submit: func(_ *nativeDialog) {
				app.libraryChange(locales.RemovingRecord(), func(context.Context) (workspace.Library, error) {
					return app.service.RemoveRepository(path)
				})
			},
		}
	}, nil
}

func (app *nativeApp) libraryOrderMenu(menu *ui.Menu) {
	for _, choice := range []struct{ value, label string }{
		{"", locales.RepositoryOrderDefault()},
		{"recent", locales.RepositoryOrderRecent()},
	} {
		selected := app.prefs.LibraryOrder == choice.value || choice.value == "" && app.prefs.LibraryOrder != "recent"
		if menu.Item(choice.label).Radio(selected).Chosen() && !selected {
			previous := app.prefs.LibraryOrder
			app.prefs.LibraryOrder = choice.value
			if err := app.savePreferences(); err != nil {
				app.prefs.LibraryOrder = previous
				app.problem = locales.SaveSettingsError(err.Error())
			}
		}
	}
}

func (app *nativeApp) libraryRelocationBlocked(paths ...string) bool {
	for _, path := range paths {
		if app.desktop != nil {
			if window := app.desktop.repositories[path]; window != nil && !window.closed {
				return true
			}
			if request := app.desktop.opening[path]; request != nil {
				if !request.source.closed {
					return true
				}
				select {
				case <-request.done:
				default:
					return true
				}
			}
		} else if app.repository != nil && app.screen != "library" && app.repository.snapshot.Path == path {
			return true
		}
	}
	return false
}

func moveLibraryPreference[Value any](values map[string]Value, oldPath, newPath string) (map[string]Value, error) {
	value, exists := values[oldPath]
	if !exists {
		return values, nil
	}
	if _, exists := values[newPath]; exists {
		return nil, errors.New(locales.RepositoryRelocationConflict())
	}
	next := maps.Clone(values)
	next[newPath] = value
	delete(next, oldPath)
	return next, nil
}

func relocatedLibraryPreferences(previous nativePreferences, oldPath, newPath string) (nativePreferences, error) {
	next := previous
	var err error
	if next.Drafts, err = moveLibraryPreference(previous.Drafts, oldPath, newPath); err != nil {
		return previous, err
	}
	if next.RepositoryGit, err = moveLibraryPreference(previous.RepositoryGit, oldPath, newPath); err != nil {
		return previous, err
	}
	if next.RepositoryAI, err = moveLibraryPreference(previous.RepositoryAI, oldPath, newPath); err != nil {
		return previous, err
	}
	if previous.LastRepository == oldPath {
		next.LastRepository = newPath
	}
	return next, nil
}

func restoreLibraryPreferencePaths[Value any](current, previous map[string]Value, paths ...string) map[string]Value {
	next := maps.Clone(current)
	for _, path := range paths {
		if value, exists := previous[path]; exists {
			if next == nil {
				next = make(map[string]Value)
			}
			next[path] = value
		} else {
			delete(next, path)
		}
	}
	return next
}

func (app *nativeApp) restoreLibraryRelocationPreferences(previous nativePreferences, oldPath, newPath string) error {
	// 只恢复这两个路径，避免回滚时覆盖其他窗口刚保存的草稿或应用设置。
	app.prefs.Drafts = restoreLibraryPreferencePaths(app.prefs.Drafts, previous.Drafts, oldPath, newPath)
	app.prefs.RepositoryGit = restoreLibraryPreferencePaths(app.prefs.RepositoryGit, previous.RepositoryGit, oldPath, newPath)
	app.prefs.RepositoryAI = restoreLibraryPreferencePaths(app.prefs.RepositoryAI, previous.RepositoryAI, oldPath, newPath)
	if app.prefs.LastRepository == newPath {
		app.prefs.LastRepository = previous.LastRepository
	}
	return app.savePreferences()
}

func (app *nativeApp) chooseRelocateRepository(entry workspace.LocalRepository) {
	if app.busy || app.closed || app.dialog != nil {
		return
	}
	if app.libraryRelocationBlocked(entry.Path) {
		app.problem = locales.RepositoryRelocationOpen()
		return
	}
	paths, err := app.chooseDirectories(locales.RelocateRepository(), locales.RelocateRepository(), false)
	if err != nil {
		app.problem = err.Error()
		return
	}
	if len(paths) == 0 {
		return
	}
	app.runTask(locales.RelocatingRepository(), true, func(ctx context.Context) (func(), error) {
		repository, err := gitrepo.Open(ctx, paths[0])
		if err != nil {
			return nil, err
		}
		return func() { app.relocateLibraryRepository(entry, repository.Path) }, nil
	})
}

func (app *nativeApp) relocateLibraryRepository(entry workspace.LocalRepository, path string) {
	if app.busy || app.closed || app.dialog != nil || app.desktop != nil && (app.desktop.checkingQuit || app.desktop.quitting) {
		return
	}
	if path == entry.Path {
		return
	}
	if app.libraryRelocationBlocked(entry.Path, path) {
		app.problem = locales.RepositoryRelocationOpen()
		return
	}
	previous := *app.prefs
	next, err := relocatedLibraryPreferences(previous, entry.Path, path)
	if err != nil {
		app.problem = err.Error()
		return
	}
	*app.prefs = next
	// 先保存偏好，失败时不修改仓库库；库提交失败再恢复这些路径的映射。
	if err := app.savePreferences(); err != nil {
		*app.prefs = previous
		app.problem = locales.SaveSettingsError(err.Error())
		return
	}
	done := make(chan struct{})
	reservation := &nativeRepositoryOpening{source: app, done: done}
	if app.desktop != nil {
		// 复用打开请求的占位规则，让其他窗口在提交期间不能打开旧路径或新路径。
		for _, reserved := range []string{entry.Path, path} {
			app.desktop.opening[reserved] = reservation
		}
	}
	app.run(locales.RelocatingRepository(), func(ctx context.Context) (func(), error) {
		defer close(done)
		library, err := app.service.RelocateRepository(ctx, entry, path)
		return func() {
			if app.desktop != nil {
				for _, reserved := range []string{entry.Path, path} {
					if app.desktop.opening[reserved] == reservation {
						delete(app.desktop.opening, reserved)
					}
				}
			}
			if err != nil {
				if restoreErr := app.restoreLibraryRelocationPreferences(previous, entry.Path, path); restoreErr != nil {
					err = fmt.Errorf("%w\n%s", err, locales.SaveSettingsError(restoreErr.Error()))
				}
				app.problem = err.Error()
				if !errors.Is(err, context.Canceled) {
					app.showProblemDetails(app.problem)
				}
				return
			}
			app.library = library
			if app.desktop == nil && app.repository != nil && app.repository.snapshot.Path == entry.Path {
				app.repository = nil
			}
		}, nil
	})
}
