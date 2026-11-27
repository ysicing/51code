// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func generatedDiffDisclosure(c *ui.Context, expanded *bool) {
	label := locales.ExpandGeneratedDiff()
	if *expanded {
		label = locales.CollapseGeneratedDiff()
	}
	c.Key("generated-diff-toggle")
	ui.Button(c, label).OnClick(func() {
		*expanded = !*expanded
	})
}
