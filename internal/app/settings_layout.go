// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import "github.com/egoist/mygo/ui"

func settingsSection(c *ui.Context, title string, content func()) {
	ui.Column(c).FillWidth().Gap(12).Children(func() {
		ui.Text(c, title).FontSize(12).Bold().TextColor(c.Theme().TextMuted)
		ui.Column(c).FillWidth().Gap(12).Children(content)
	})
}

func settingsRow(c *ui.Context, label string, content func()) {
	ui.Row(c).FillWidth().MinHeight(32).Gap(16).AlignItems(ui.Start).Children(func() {
		ui.Text(c, label).FontSize(13).Width(96).Shrink(0).MaxLines(2).Margin(6, 0, 0).Tooltip(label)
		ui.Column(c).Grow(1).MinWidth(0).MinHeight(28).Gap(8).Justify(ui.Center).Children(content)
	})
}
