// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"slices"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func remoteBranchParts(reference string, remotes []gitrepo.RemoteConfig) (string, string) {
	remote, name, found := strings.Cut(reference, "/")
	if !found {
		name = reference
	}
	for _, config := range remotes {
		if len(config.Name) > len(remote) && strings.HasPrefix(reference, config.Name+"/") {
			remote, name = config.Name, strings.TrimPrefix(reference, config.Name+"/")
		}
	}
	return remote, name
}

func (app *nativeApp) remoteSidebar(c *ui.Context) {
	services := c.Services()
	app.referenceSidebar(c, "远端", "远端分支树", nativeIcons["cloud"], app.remoteNodes, func(_ *nativeRefTree, reference string, item ui.Element) {
		_, name := remoteBranchParts(reference, app.repository.remotes)
		if name == "HEAD" {
			item.Tooltip(reference + " · " + locales.RemoteHead())
		} else {
			item.ContextMenu(func(menu *ui.Menu) {
				if menu.Item(locales.CheckoutRemoteBranch()).Disabled(!app.writable || app.dialog != nil).Chosen() {
					app.checkoutDialog(reference)
				}
				if menu.Item(locales.MergeBranchInto(reference, app.repository.snapshot.Branch)).
					Disabled(!app.canMerge() || app.dialog != nil).Chosen() {
					app.mergeDialogFrom("refs/remotes/" + reference)
				}
				menu.Separator()
				if menu.Item(locales.CopyBranchName()).Chosen() {
					services.WriteClipboard(reference)
				}
			})
			item.OnClick(func() { app.selectHistoryReference("远端", reference, false) })
		}
	})
}

// remoteNodes 按远端分组构建远端分支节点，远端没有分支时也保留分组。
func (app *nativeApp) remoteNodes(refs *nativeRefTree) {
	groups := map[string][]string{}
	defaults := map[string]string{}
	for _, remote := range app.repository.remotes {
		groups[remote.Name] = nil
	}
	for _, branch := range app.repository.branches {
		if branch.Remote {
			remote, name := remoteBranchParts(branch.Name, app.repository.remotes)
			groups[remote] = append(groups[remote], name)
			if branch.Default {
				defaults[remote] = name
			}
		}
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	slices.Sort(names)
	refs.nodes = nil
	for _, remote := range names {
		refs.nodes = append(refs.nodes, &nativeRefNode{
			name: remote, reference: remote, remote: true,
			children: nativeReferenceNodes(groups[remote], remote+"/", "", defaults[remote]),
		})
	}
}
