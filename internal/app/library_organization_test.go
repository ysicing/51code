// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ysicing/51code/internal/workspace"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestNativeLibraryOrganizationSortPersistsAndKeepsFilters(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.showLibrary()
	app.library = workspace.Library{
		Groups: []workspace.Group{{ID: "work", Name: "工作"}},
		Repositories: []workspace.LocalRepository{
			{Name: "旧仓库", Path: "/legacy", GroupID: "work"},
			{Name: "新仓库", Path: "/newest", GroupID: "work", LastOpenedAt: 200},
			{Name: "其他分组", Path: "/other", GroupID: "other", LastOpenedAt: 300},
		},
	}
	app.group = "work"
	tester := ui.NewTester(app.view, 960, 640)
	legacy, legacyFound := tester.AccessByRole(ui.RoleButton, "旧仓库")
	newest, newestFound := tester.AccessByRole(ui.RoleButton, "新仓库")
	if !legacyFound || !newestFound || legacy.Bounds.Y >= newest.Bounds.Y {
		t.Fatal("default sort changed legacy order")
	}
	if err := tester.Click(locales.RepositoryOrder()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tester.Menu(), []string{locales.RepositoryOrderDefault(), locales.RepositoryOrderRecent()}) {
		t.Fatal("sort did not use a compact menu", tester.Menu())
	}
	if err := tester.ChooseMenuItem(locales.RepositoryOrderRecent()); err != nil {
		t.Fatal(err)
	}
	legacy, _ = tester.AccessByRole(ui.RoleButton, "旧仓库")
	newest, _ = tester.AccessByRole(ui.RoleButton, "新仓库")
	if newest.Bounds.Y >= legacy.Bounds.Y || app.prefs.LibraryOrder != "recent" || app.group != "work" {
		t.Fatal("recent sort lost group filter or failed to reorder")
	}
	if _, found := tester.AccessByRole(ui.RoleButton, "其他分组"); found {
		t.Fatal("sort ignored the current group")
	}
	restored := newNativeApp(app.service, app.stateFile)
	t.Cleanup(restored.cancel)
	if restored.prefs.LibraryOrder != "recent" {
		t.Fatal("sort choice was not persisted")
	}
	app.query = "/legacy"
	tester.Frame()
	if _, found := tester.AccessByRole(ui.RoleButton, "新仓库"); found {
		t.Fatal("sort ignored repository search")
	}
	if app.library.Repositories[0].Path != "/legacy" {
		t.Fatal("sort rewrote the stored insertion order")
	}
}

func TestNativeLibraryOrganizationSortFailureAndCompactLayout(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.showLibrary()
	for _, appearance := range []string{appearanceLight, appearanceDark} {
		app.prefs.Appearance = appearance
		app.library.Repositories[0].Name = strings.Repeat("长仓库名称", 12)
		tester := ui.NewTester(app.view, 800, 600)
		button, found := tester.AccessByRole(ui.RoleMenuButton, locales.RepositoryOrder())
		if !found || button.Offscreen || button.Bounds.X+button.Bounds.W > 800 {
			t.Fatal("sort menu is clipped in compact library")
		}
		app.busy = true
		tester.Frame()
		button, _ = tester.AccessByRole(ui.RoleMenuButton, locales.RepositoryOrder())
		if !button.Disabled {
			t.Fatal("sort menu remained enabled during a task")
		}
		app.busy = false
	}
	app.stateFile = t.TempDir()
	tester := ui.NewTester(app.view, 960, 640)
	if err := tester.Click(locales.RepositoryOrder()); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(locales.RepositoryOrderRecent()); err != nil {
		t.Fatal(err)
	}
	if app.prefs.LibraryOrder != "" || app.problem == "" {
		t.Fatal("failed sort preference save was not rolled back")
	}
}

func TestNativeLibraryOrganizationPreferenceMappingProtectsDestination(t *testing.T) {
	enabled := true
	original := nativePreferences{
		LastRepository: "/old",
		Drafts:         map[string]string{"/old": "保留草稿", "/other": "其他草稿"},
		RepositoryGit:  map[string]nativeGitOverride{"/old": {LFS: &enabled}},
		RepositoryAI:   map[string]nativeAIOverride{"/old": {Language: "zh"}},
	}
	next, err := relocatedLibraryPreferences(original, "/old", "/new")
	if err != nil || next.LastRepository != "/new" || next.Drafts["/new"] != "保留草稿" ||
		next.RepositoryGit["/new"].LFS != &enabled || next.RepositoryAI["/new"].Language != "zh" {
		t.Fatalf("path preferences not mapped: %+v %v", next, err)
	}
	if original.Drafts["/old"] != "保留草稿" || original.RepositoryGit["/old"].LFS != &enabled ||
		original.RepositoryAI["/old"].Language != "zh" {
		t.Fatal("preparing relocation mutated shared preferences")
	}
	for _, kind := range []string{"draft", "git", "ai"} {
		t.Run(kind, func(t *testing.T) {
			collision := original
			switch kind {
			case "draft":
				collision.Drafts = map[string]string{"/old": "旧草稿", "/new": "目标草稿"}
			case "git":
				collision.RepositoryGit = map[string]nativeGitOverride{"/old": {}, "/new": {}}
			case "ai":
				collision.RepositoryAI = map[string]nativeAIOverride{"/old": {}, "/new": {}}
			}
			got, err := relocatedLibraryPreferences(collision, "/old", "/new")
			if err == nil || !reflect.DeepEqual(got, collision) {
				t.Fatal("destination preferences were overwritten")
			}
		})
	}
}

func nativeRelocationFixture(t *testing.T) (*nativeApp, workspace.LocalRepository, string, func()) {
	t.Helper()
	app, _, finish := nativeFixture(t)
	app.showLibrary()
	path := app.library.Repositories[0].Path
	library, err := app.service.CreateGroup("工作")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.service.MoveRepository(path, library.Groups[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.service.MarkRepositoryOpened(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "relocated")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	app.library, err = app.service.RefreshLibrary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entry := app.library.Repositories[0]
	app.prefs.Drafts[path] = "保留草稿"
	app.prefs.RepositoryAI[path] = nativeAIOverride{Language: "zh"}
	app.prefs.LastRepository = path
	return app, entry, target, finish
}

func TestNativeLibraryOrganizationRelocatePickerPreservesGroupAndPreferences(t *testing.T) {
	app, entry, target, finish := nativeRelocationFixture(t)
	app.openDialog = func(options mygo.OpenDialogOptions) ([]string, error) {
		if !app.busy || !options.Directory || options.Multiple || options.Title != locales.RelocateRepository() {
			t.Fatal("relocation did not follow the directory picker convention", options)
		}
		return []string{target}, nil
	}
	tester := ui.NewTester(app.view, 960, 640)
	if err := tester.Click(locales.Manage() + " " + entry.Name); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(locales.RelocateRepositoryMenu()); err != nil {
		t.Fatal(err)
	}
	finish()
	if !app.busy {
		t.Fatal("canonical path validation did not schedule relocation")
	}
	finish()
	tester.Frame()
	current := app.library.Repositories[0]
	if current.Path != target || current.GroupID != entry.GroupID || current.LastOpenedAt != entry.LastOpenedAt ||
		current.Problem != "" || app.problem != "" || app.prefs.LastRepository != target ||
		app.prefs.Drafts[target] != "保留草稿" || app.prefs.RepositoryAI[target].Language != "zh" {
		t.Fatalf("relocation lost data: %+v, %s", current, app.problem)
	}
	if _, exists := app.prefs.Drafts[entry.Path]; exists {
		t.Fatal("old path draft was not migrated")
	}
	restored := newNativeApp(app.service, app.stateFile)
	t.Cleanup(restored.cancel)
	if restored.prefs.Drafts[target] != "保留草稿" || restored.prefs.LastRepository != target {
		t.Fatal("relocated preferences did not persist")
	}
}

func TestNativeLibraryOrganizationRelocateCancelAndFailuresKeepEntry(t *testing.T) {
	for _, kind := range []string{"cancel", "picker", "invalid", "preferences", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			app, entry, target, finish := nativeRelocationFixture(t)
			switch kind {
			case "invalid":
				target = t.TempDir()
			case "preferences":
				app.stateFile = t.TempDir()
			case "duplicate":
				if _, err := app.service.AddRepositories(context.Background(), []string{target}, ""); err != nil {
					t.Fatal(err)
				}
				app.library = app.service.Library()
			}
			before := app.service.Library()
			app.openDialog = func(mygo.OpenDialogOptions) ([]string, error) {
				switch kind {
				case "cancel":
					return nil, nil
				case "picker":
					return nil, errors.New("picker failure")
				default:
					return []string{target}, nil
				}
			}
			app.chooseRelocateRepository(entry)
			if kind == "invalid" || kind == "duplicate" {
				finish()
			}
			if kind == "duplicate" {
				app.prefs.Drafts["/unrelated"] = "并行草稿"
				app.prefs.LibraryOrder = "recent"
				finish()
				if app.prefs.Drafts["/unrelated"] != "并行草稿" || app.prefs.LibraryOrder != "recent" {
					t.Fatal("rollback overwrote unrelated shared preferences")
				}
			}
			if !reflect.DeepEqual(before, app.service.Library()) || app.prefs.Drafts[entry.Path] != "保留草稿" ||
				app.prefs.LastRepository != entry.Path || app.busy {
				t.Fatal("failed or cancelled relocation changed the entry or preferences")
			}
			if _, exists := app.prefs.Drafts[target]; exists {
				t.Fatal("failed relocation left mapped preferences")
			}
			if kind != "cancel" && app.problem == "" {
				t.Fatal("relocation failure was not reported")
			}
		})
	}
}

func TestNativeLibraryOrganizationRelocateReservesPathsAndBlocksOpenWindows(t *testing.T) {
	app, entry, target, finish := nativeRelocationFixture(t)
	desktop := &nativeDesktop{
		service: app.service, prefs: app.prefs, applied: *app.prefs, library: app,
		repositories: map[string]*nativeApp{}, opening: map[string]*nativeRepositoryOpening{},
	}
	app.desktop = desktop
	app.repository = nil
	desktop.repositories[entry.Path] = &nativeApp{}
	picked := false
	app.openDialog = func(mygo.OpenDialogOptions) ([]string, error) {
		picked = true
		return []string{target}, nil
	}
	app.chooseRelocateRepository(entry)
	if picked || app.busy || app.problem != locales.RepositoryRelocationOpen() {
		t.Fatal("relocation of an open repository was allowed")
	}
	delete(desktop.repositories, entry.Path)
	pending := make(chan struct{})
	desktop.opening[entry.Path] = &nativeRepositoryOpening{source: app, done: pending}
	if !app.libraryRelocationBlocked(entry.Path) {
		t.Fatal("pending open request did not block relocation")
	}
	close(pending)
	delete(desktop.opening, entry.Path)
	app.relocateLibraryRepository(entry, target)
	if !app.busy || desktop.opening[entry.Path] == nil ||
		desktop.opening[entry.Path] != desktop.opening[target] ||
		!app.libraryRelocationBlocked(entry.Path, target) {
		t.Fatal("relocation did not reserve both paths against concurrent opens")
	}
	finish()
	if app.problem != "" || len(desktop.opening) != 0 || app.library.Repositories[0].Path != target {
		t.Fatalf("relocation did not release reservations: %+v %s", desktop.opening, app.problem)
	}
}
