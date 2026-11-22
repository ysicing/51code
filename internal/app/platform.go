// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"runtime"
	"slices"
	"strings"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
)

const desktopReleasesURL = "https://github.com/ysicing/51code/releases/latest"

var pcShortcutLabels = strings.NewReplacer("CmdOrCtrl+", "Ctrl+")

func shortcutLabelForPlatform(shortcut, platform string) string {
	if platform != "darwin" {
		return pcShortcutLabels.Replace(shortcut)
	}
	// macOS 按系统惯例以 ⌃⌥⇧⌘ 的顺序显示修饰键，与存储顺序无关。
	parts := strings.Split(shortcut, "+")
	mods, key := parts[:len(parts)-1], parts[len(parts)-1]
	var label string
	for _, mod := range []struct{ name, symbol string }{
		{"Ctrl", "⌃"}, {"Alt", "⌥"}, {"Shift", "⇧"}, {"CmdOrCtrl", "⌘"},
	} {
		if slices.Contains(mods, mod.name) {
			label += mod.symbol
		}
	}
	if key == "Enter" {
		key = "↩"
	}
	return label + key
}

func shortcutLabel(shortcut string) string {
	return shortcutLabelForPlatform(shortcut, runtime.GOOS)
}

func newNativeApplicationMenu(items []*mygo.MenuItem) *mygo.Menu {
	items = platformMenu(items, runtime.GOOS)
	menu := mygo.NewMenu(items)
	for _, item := range items {
		if item.Role == mygo.RoleViewMenu {
			for _, child := range item.Submenu {
				if child.Role == mygo.RoleReload || child.Role == mygo.RoleForceReload {
					// 原生窗口没有网页可重载（MyGo 对原生窗口忽略这两个角色），
					// 释放 ⌘R／⌘⇧R 给仓库刷新等可配置操作。
					child.SetAccelerator("")
				}
			}
		}
	}
	return menu
}

func platformMenu(items []*mygo.MenuItem, platform string) []*mygo.MenuItem {
	if platform == "darwin" {
		return items
	}
	application := make([]*mygo.MenuItem, 0, len(items[0].Submenu))
	for _, item := range items[0].Submenu {
		switch item.Role {
		case mygo.RoleServices, mygo.RoleHide, mygo.RoleHideOthers, mygo.RoleUnhide:
			continue
		}
		if item.Type == mygo.MenuItemSeparator &&
			(len(application) == 0 || application[len(application)-1].Type == mygo.MenuItemSeparator) {
			continue
		}
		application = append(application, item)
	}
	if len(application) > 0 && application[len(application)-1].Type == mygo.MenuItemSeparator {
		application = application[:len(application)-1]
	}
	items[0].Submenu = application
	return items
}

func revealRepositoryLabel() string {
	if runtime.GOOS == "darwin" {
		return locales.RevealFinder()
	}
	return locales.OpenRepositoryFolder()
}

func credentialKeyHint() string {
	if runtime.GOOS == "darwin" {
		return locales.KeychainKeyHint()
	}
	return locales.CredentialKeyHint()
}
