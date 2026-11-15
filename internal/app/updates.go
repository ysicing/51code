// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"strings"
	"sync"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/ui"
)

// selfUpdates 报告当前安装能否自更新：构建带更新源且安装目录可写。deb/rpm 装在 /opt
// 由包管理器负责、开发构建没有更新源，均为 false，更新入口改为打开 GitHub Releases。
// 安装位置在运行期间不变，只探测一次（探测会在安装目录建删临时目录）；测试可替换。
var selfUpdates = sync.OnceValue(mygo.Updater.Enabled)

// checkUpdates 是版本号按钮的动作，与菜单和设置页的更新入口保持一致。
func (app *nativeApp) checkUpdates(c *ui.Context) {
	if selfUpdates() {
		updater.CheckForUpdates()
		return
	}
	c.OpenURLThen(desktopReleasesURL, func(err error) {
		if err != nil {
			app.problem = err.Error()
		}
	})
}

func (app *nativeApp) versionButton(c *ui.Context) bool {
	label := locales.CheckUpdates()
	text := buildVersionLabel()
	color := c.Theme().TextMuted
	tooltip := label + "\n" + locales.CurrentVersion(text)
	if app.availableVersion != "" {
		text = locales.NewVersion(app.availableVersion)
		label = locales.NewVersionCheck(text)
		tooltip += "\n" + text
		color = c.Theme().Accent
	}
	tooltip = strings.TrimSpace(tooltip + "\n" + buildDetails())
	button := ui.ButtonBase(c).Label(label).Tooltip(tooltip).
		Padding(4, 6).Gap(4).Radius(c.Theme().Radius).Shrink(0).TextColor(color)
	if button.Pressed() {
		button.Background(c.Theme().SurfacePressed)
	} else if button.Hovered() {
		button.Background(c.Theme().SurfaceHover)
	}
	return button.Children(func() {
		if app.availableVersion != "" {
			ui.Icon(c, nativeIcons["pull"]).FontSize(12)
		}
		ui.Text(c, text).FontSize(11).SingleLine()
	}).Clicked()
}

func (app *nativeApp) manualUpdatePreferences(c *ui.Context) {
	if ui.Button(c, locales.DownloadRelease()).Clicked() {
		app.settingsError = ""
		c.OpenURLThen(desktopReleasesURL, func(err error) {
			if err != nil {
				app.settingsError = err.Error()
			}
		})
	}
}
