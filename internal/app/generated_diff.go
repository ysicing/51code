// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func generatedDiffDisclosure(c *ui.Context, expanded *bool) bool {
	label := locales.ExpandGeneratedDiff()
	if *expanded {
		label = locales.CollapseGeneratedDiff()
	}
	ui.Row(c).Key("generated-diff-disclosure").FillWidth().Padding(8, 12).Children(func() {
		ui.Button(c, label).OnClick(func() {
			*expanded = !*expanded
		})
	})
	return !*expanded
}
