// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

type nativeSidebarItems struct {
	Insights  bool `json:"insights"`
	Search    bool `json:"search"`
	Compare   bool `json:"compare"`
	Stash     bool `json:"stash"`
	Worktrees bool `json:"worktrees"`
}

func (app *nativeApp) reconcileSidebar() {
	// 全局开关可能由其他窗口修改，先保留进行中的操作和未保存编辑，空闲后再退出已隐藏页面。
	if app.busy || app.dialog != nil || app.conflictDirty() {
		return
	}
	visible := true
	switch app.screen {
	case "insights":
		visible = app.prefs.SidebarItems.Insights
	case "search":
		visible = app.prefs.SidebarItems.Search
	case "compare":
		visible = app.prefs.SidebarItems.Compare
	case "stash":
		visible = app.prefs.SidebarItems.Stash
	case "worktrees":
		visible = app.prefs.SidebarItems.Worktrees
	}
	if !visible {
		app.switchWorkspace("changes")
	}
}

func (app *nativeApp) sidebarPreference(c *ui.Context, screen, label string, visible *bool) {
	c.Key("sidebar-preference:" + screen)
	previous := *visible
	ui.Checkbox(c, visible, label).OnClick(func() {
		app.settingsError = ""
		if err := app.savePreferences(); err != nil {
			*visible = previous
			app.settingsError = locales.SaveSettingsError(err.Error())
			return
		}
		if !*visible && app.screen == screen {
			app.switchWorkspace("changes")
		}
	})
}
