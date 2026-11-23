// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ysicing/51code/internal/workspace"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestNativeCloneSuggestsDirectoryFromRemoteAddress(t *testing.T) {
	app, _, _ := nativeFixture(t)
	app.showLibrary()
	tester := ui.NewTester(app.view, 1024, 680)
	if err := tester.Click(locales.Clone()); err != nil {
		t.Fatal(err)
	}
	replace := func(label, value string) {
		t.Helper()
		field, found := tester.AccessByRole(ui.RoleTextField, label)
		if !found || field.Offscreen {
			t.Fatalf("missing clone field %q", label)
		}
		tester.ClickAt(field.Bounds.X+field.Bounds.W/2, field.Bounds.Y+field.Bounds.H/2)
		tester.Key(ui.Cmd, ui.KeyA)
		tester.Type(value)
	}
	address, directory := locales.RemoteAddress(), locales.CloneDirectoryName()
	requireDirectory := func(want string) {
		t.Helper()
		field, found := tester.AccessByRole(ui.RoleTextField, directory)
		if !found || field.Value != want {
			t.Fatalf("clone directory = %q, want %q (field found: %v)", field.Value, want, found)
		}
	}
	replace(address, "git@code.example:wxapp/jika.git")
	requireDirectory("jika")
	replace(address, "https://code.example")
	requireDirectory("")
	replace(address, "https://code.example/team/another.git")
	requireDirectory("another")
	replace(directory, "my-checkout")
	replace(address, "ssh://git@code.example:2222/team/third.git")
	requireDirectory("my-checkout")
}

func TestNativeCloneAddsAndOpensInCurrentGroup(t *testing.T) {
	app, source, finish := nativeFixture(t)
	grouped, err := app.service.CreateGroup("克隆测试")
	if err != nil {
		t.Fatal(err)
	}
	app.library, app.group = grouped, grouped.Groups[0].ID
	app.showLibrary()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.openDialog = func(options mygo.OpenDialogOptions) ([]string, error) {
		if !options.Directory || options.Multiple {
			t.Fatalf("unexpected picker options: %+v", options)
		}
		return []string{parent}, nil
	}
	tester := ui.NewTester(app.view, 1024, 680)
	click := func(label string) {
		t.Helper()
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	click(locales.Clone())
	focus := func(label string) {
		t.Helper()
		field, found := tester.AccessByRole(ui.RoleTextField, label)
		if !found || field.Offscreen {
			t.Fatalf("missing clone field %q", label)
		}
		tester.ClickAt(field.Bounds.X+field.Bounds.W/2, field.Bounds.Y+field.Bounds.H/2)
	}
	focus(locales.RemoteAddress())
	tester.Type(source)
	focus(locales.CloneDirectoryName())
	tester.Type("cloned")
	click(locales.ChooseCloneLocation())
	if app.dialog.fields[2].value != parent {
		t.Fatal("folder selection did not update clone destination")
	}
	if !app.dialog.valid() {
		t.Fatal("clone form did not accept the entered values")
	}
	button, found := tester.AccessByRole(ui.RoleButton, locales.Clone())
	if !found || button.Disabled {
		t.Fatal("missing enabled clone submit button")
	}
	tester.ClickAt(button.Bounds.X+button.Bounds.W/2, button.Bounds.Y+button.Bounds.H/2)
	if !app.busy || app.operationCancel == nil || app.remoteProgress == nil {
		t.Fatal("clone progress and cancellation were not exposed")
	}
	for app.busy {
		finish()
	}
	target := filepath.Join(parent, "cloned")
	if app.problem != "" || app.dialog != nil || app.screen != "changes" || app.repository.snapshot.Path != target {
		t.Fatalf("cloned repository did not open: screen=%s, problem=%s", app.screen, app.problem)
	}
	if !slices.ContainsFunc(app.library.Repositories, func(entry workspace.LocalRepository) bool {
		return entry.Path == target && entry.GroupID == app.group
	}) {
		t.Fatal("clone did not join the current group")
	}
	if _, err := os.Stat(filepath.Join(target, "hello.txt")); err != nil {
		t.Fatal(err)
	}
}
