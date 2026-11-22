// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"runtime"
	"strconv"
	"strings"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

const (
	shortcutLibrary  = "shortcut-library"
	shortcutAddRepo  = "shortcut-add-repository"
	shortcutScan     = "shortcut-scan"
	shortcutRefresh  = "shortcut-refresh"
	shortcutCommit   = "shortcut-commit"
	shortcutQuick    = "shortcut-quick-open"
	settingsShortcut = "CmdOrCtrl+,"
)

type nativeShortcuts struct {
	Library       string `json:"library"`
	AddRepository string `json:"addRepository"`
	Scan          string `json:"scan"`
	Refresh       string `json:"refresh"`
	Commit        string `json:"commit"`
	QuickOpen     string `json:"quickOpen"`
}

func defaultNativeShortcuts() nativeShortcuts {
	return nativeShortcuts{
		Library: "CmdOrCtrl+Shift+L", AddRepository: "CmdOrCtrl+O", Scan: "CmdOrCtrl+Shift+O",
		Refresh: "CmdOrCtrl+R", Commit: "CmdOrCtrl+Enter", QuickOpen: "CmdOrCtrl+P",
	}
}

func (shortcuts *nativeShortcuts) bindings() map[string]*string {
	return map[string]*string{
		shortcutLibrary: &shortcuts.Library,
		shortcutAddRepo: &shortcuts.AddRepository,
		shortcutScan:    &shortcuts.Scan,
		shortcutRefresh: &shortcuts.Refresh,
		shortcutCommit:  &shortcuts.Commit,
		shortcutQuick:   &shortcuts.QuickOpen,
	}
}

func normalizeNativeShortcuts(shortcuts nativeShortcuts) nativeShortcuts {
	defaults := defaultNativeShortcuts()
	fallbacks := defaults.bindings()
	seen := make(map[string]bool, len(fallbacks))
	for id, value := range shortcuts.bindings() {
		fallback := *fallbacks[id]
		if *value == "" {
			*value = fallback
		}
		*value = shortcutAccelerator(*value)
		if *value == "" || reservedNativeShortcuts[*value] || seen[*value] {
			return defaults
		}
		seen[*value] = true
	}
	return shortcuts
}

var shortcutKeyNames = func() map[ui.Key]string {
	names := map[ui.Key]string{
		ui.KeyEnter: "Enter", ui.KeyComma: ",", ui.KeyPeriod: ".", ui.KeySlash: "/",
		ui.KeySemicolon: ";", ui.KeyQuote: "'", ui.KeyBracketLeft: "[", ui.KeyBracketRight: "]",
		ui.KeyMinus: "-", ui.KeyEqual: "=", ui.KeyBackquote: "`",
	}
	for key := ui.KeyA; key <= ui.KeyZ; key++ {
		names[key] = string('A' + rune(key-ui.KeyA))
	}
	for key := ui.Key0; key <= ui.Key9; key++ {
		names[key] = string('0' + rune(key-ui.Key0))
	}
	for key := ui.KeyF1; key <= ui.KeyF12; key++ {
		names[key] = "F" + strconv.Itoa(int(key-ui.KeyF1)+1)
	}
	return names
}()

var legacyShortcutSymbols = strings.NewReplacer("⌘", "CmdOrCtrl+", "⇧", "Shift+", "⌥", "Alt+", "↩", "Enter")

func shortcutAccelerator(shortcut string) string {
	parts := strings.Split(legacyShortcutSymbols.Replace(shortcut), "+")
	var mods ui.Modifiers
	for _, prefix := range parts[:len(parts)-1] {
		var mod ui.Modifiers
		switch strings.ToLower(prefix) {
		case "cmdorctrl", "cmd", "ctrl":
			mod = ui.Cmd
		case "shift":
			mod = ui.Shift
		case "alt":
			mod = ui.Alt
		default:
			return ""
		}
		if mods&mod != 0 {
			return ""
		}
		mods |= mod
	}
	if mods&ui.Cmd == 0 {
		return ""
	}
	key := parts[len(parts)-1]
	if strings.EqualFold(key, "return") {
		key = "Enter"
	}
	for _, name := range shortcutKeyNames {
		if strings.EqualFold(key, name) {
			return shortcutPrefix(mods) + name
		}
	}
	return ""
}

func shortcutPrefix(mods ui.Modifiers) string {
	var prefix string
	if mods&ui.Cmd != 0 {
		prefix = "CmdOrCtrl+"
	}
	if ui.Cmd != ui.Ctrl && mods&ui.Ctrl != 0 {
		prefix += "Ctrl+"
	}
	if ui.Cmd != ui.Super && mods&ui.Super != 0 {
		prefix += "Super+"
	}
	if mods&ui.Shift != 0 {
		prefix += "Shift+"
	}
	if mods&ui.Alt != 0 {
		prefix += "Alt+"
	}
	return prefix
}

var reservedNativeShortcuts = func() map[string]bool {
	// app.go 的设置菜单、应用及窗口角色、MyGo 的 Edit/View 子菜单，以及文本查找键。
	reserved := map[string]bool{
		settingsShortcut: true,
		"CmdOrCtrl+Q":    true, "CmdOrCtrl+W": true, "CmdOrCtrl+H": true,
		"CmdOrCtrl+M": true, "CmdOrCtrl+Alt+H": true,
		"CmdOrCtrl+Z": true, "CmdOrCtrl+Shift+Z": true, "CmdOrCtrl+X": true,
		"CmdOrCtrl+C": true, "CmdOrCtrl+V": true, "CmdOrCtrl+A": true,
		"CmdOrCtrl+0": true, "CmdOrCtrl+-": true,
		"CmdOrCtrl+F": true, "CmdOrCtrl+G": true, "CmdOrCtrl+Shift+G": true,
	}
	switch runtime.GOOS {
	case "darwin":
		reserved["CmdOrCtrl+Shift+Alt+V"] = true
		reserved["CmdOrCtrl+Alt+I"] = true
		// AppKit 后端为放大角色额外注册了隐藏的 Cmd+=。
		reserved["CmdOrCtrl+="] = true
	case "windows":
		reserved["CmdOrCtrl+Y"] = true
	}
	if runtime.GOOS != "darwin" {
		reserved["CmdOrCtrl+Shift+I"] = true
	}
	for _, binding := range editor.DefaultBindings() {
		if name := shortcutKeyNames[binding.Key]; name != "" && binding.ThenKey == 0 &&
			binding.Mods&ui.Cmd != 0 && binding.Mods & ^(ui.Cmd|ui.Shift|ui.Alt) == 0 {
			reserved[shortcutPrefix(binding.Mods)+name] = true
		}
	}
	// 旧版下拉框提供过这些组合，其中部分与编辑器绑定重叠，但菜单快捷键优先于编辑器。
	// 继续放行，避免升级后用户已有的设置被判为保留而整组恢复默认。
	for _, legacy := range []string{
		"CmdOrCtrl+Shift+L", "CmdOrCtrl+O", "CmdOrCtrl+Shift+O", "CmdOrCtrl+R", "CmdOrCtrl+Enter", "CmdOrCtrl+Shift+R",
		"CmdOrCtrl+Shift+Enter", "CmdOrCtrl+Alt+L", "CmdOrCtrl+Alt+R", "CmdOrCtrl+P", "CmdOrCtrl+Shift+P", "CmdOrCtrl+Alt+P",
	} {
		delete(reserved, legacy)
	}
	return reserved
}()

func applyNativeShortcuts(shortcuts nativeShortcuts) {
	menu := mygo.App.Menu()
	if menu == nil {
		return
	}
	for id, value := range shortcuts.bindings() {
		if item := menu.ItemByID(id); item != nil {
			item.SetAccelerator(*value)
		}
	}
	if runtime.GOOS != "darwin" {
		// Windows 和 GTK 在安装菜单时重建快捷键绑定。
		mygo.App.SetMenu(menu)
	}
}

func (app *nativeApp) stopShortcutRecording() {
	if app.shortcutRecording != nil {
		app.shortcutRecording = nil
		applyNativeShortcuts(app.prefs.Shortcuts)
	}
}

func (app *nativeApp) saveShortcutChanges(previous nativeShortcuts) {
	app.settingsError = ""
	if err := app.savePreferences(); err != nil {
		app.prefs.Shortcuts = previous
		app.settingsError = locales.SaveSettingsError(err.Error())
	}
	app.shortcutRecording = nil
	applyNativeShortcuts(app.prefs.Shortcuts)
}

func (app *nativeApp) shortcutPreference(c *ui.Context, label string, value *string) {
	settingsRow(c, label, func() {
		text := shortcutLabel(*value)
		if app.shortcutRecording == value {
			text = strings.TrimSuffix(shortcutLabel(shortcutPrefix(c.Modifiers())), "+")
			if text == "" {
				text = locales.RecordShortcut()
			}
		}
		c.Key(value)
		button := ui.Button(c, text).Label(locales.ShortcutLabel(label)).Value(text).Width(210).
			OnClick(func() {
				if app.shortcutRecording == value {
					return
				}
				app.settingsError = ""
				app.shortcutRecording = value
				applyNativeShortcuts(nativeShortcuts{})
			}).HandleInput(func(event ui.InputEvent) bool {
			if app.shortcutRecording != value {
				return false
			}
			return app.recordShortcutInput(event, value)
		})
		if app.shortcutRecording == value && (button.FocusLeft() || button.PressedOutside()) {
			app.stopShortcutRecording()
		}
	})
}

func (app *nativeApp) recordShortcutInput(event ui.InputEvent, value *string) bool {
	if event.Kind == ui.InputCommand {
		app.settingsError = locales.ReservedShortcut()
		app.stopShortcutRecording()
		return true
	}
	if event.Kind != ui.InputKeyDown {
		return false
	}
	key := event.PhysicalKey
	if key == ui.KeyUnknown {
		key = event.Key
	}
	switch {
	case event.Repeat, event.Code >= ui.CodeControlLeft && event.Code <= ui.CodeSuperRight:
		return true
	case key == ui.KeyEscape && event.Mods == 0:
		app.stopShortcutRecording()
		return true
	case key == ui.KeyTab && (event.Mods == 0 || event.Mods == ui.Shift):
		return false
	}
	name := shortcutKeyNames[key]
	accelerator := shortcutPrefix(event.Mods) + name
	switch {
	case name == "", event.Mods&ui.Cmd == 0, event.Mods & ^(ui.Cmd|ui.Shift|ui.Alt) != 0:
		app.settingsError = locales.UnsupportedShortcut(strings.TrimSuffix(shortcutLabel("CmdOrCtrl+"), "+"))
	case reservedNativeShortcuts[accelerator]:
		app.settingsError = locales.ReservedShortcut()
	default:
		for _, other := range app.prefs.Shortcuts.bindings() {
			if other != value && *other == accelerator {
				app.settingsError = locales.DuplicateShortcuts()
				app.stopShortcutRecording()
				return true
			}
		}
		previous := app.prefs.Shortcuts
		*value = accelerator
		app.saveShortcutChanges(previous)
		return true
	}
	app.stopShortcutRecording()
	return true
}
