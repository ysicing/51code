// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

type nativeComparison struct {
	base, target    string
	mergeBase       bool
	result          *gitrepo.Comparison
	resultBase      string
	resultTarget    string
	resultMergeBase bool
	file            string
	selected        int
	list            *ui.ListState
	diff            gitrepo.Diff
	references      nativeComparisonReferences
}

func (state *nativeComparison) restoreSelection() {
	state.selected = -1
	if state.result != nil {
		for index, file := range state.result.Files {
			if file.Path == state.file {
				state.selected = index
				break
			}
		}
	}
}

type comparisonReader struct {
	compare func(context.Context, string, string, bool) (gitrepo.Comparison, error)
	diff    func(context.Context, string, string, gitrepo.CommitFile) (gitrepo.Diff, error)
}

func (app *nativeApp) comparisonReader() comparisonReader {
	if app.ssh != nil {
		return comparisonReader{app.ssh.repo.Compare, app.ssh.repo.ComparisonDiff}
	}
	path, service := app.repository.snapshot.Path, app.service
	return comparisonReader{
		func(ctx context.Context, base, target string, mergeBase bool) (gitrepo.Comparison, error) {
			return service.Compare(ctx, path, base, target, mergeBase)
		},
		func(ctx context.Context, from, target string, file gitrepo.CommitFile) (gitrepo.Diff, error) {
			return service.CompareFileDiff(ctx, path, from, target, file.Path)
		},
	}
}

func (app *nativeApp) startComparison() {
	if app.repository == nil && app.ssh == nil || app.screen != "compare" || app.comparison == nil || app.busy || app.dialog != nil {
		return
	}
	state := app.comparison
	reader, base, target, mergeBase := app.comparisonReader(), state.base, state.target, state.mergeBase
	app.load(locales.ReadingComparison(), nativeReadCommit, func(ctx context.Context) (func(), error) {
		result, err := reader.compare(ctx, base, target, mergeBase)
		if err != nil {
			return nil, err
		}
		file := ""
		var diff gitrepo.Diff
		var diffErr error
		if len(result.Files) > 0 {
			file = result.Files[0].Path
			diff, diffErr = reader.diff(ctx, result.From, result.Target, result.Files[0])
		}
		if diffErr != nil {
			return nil, diffErr
		}
		return func() {
			state.result, state.resultBase, state.resultTarget, state.resultMergeBase = &result, base, target, mergeBase
			state.file, state.diff, state.selected, state.list = file, diff, -1, &ui.ListState{}
			if len(result.Files) > 0 {
				state.selected = 0
			}
			app.clearDiff()
			app.setDiff(diff)
		}, diffErr
	})
}

func (app *nativeApp) selectComparisonFile(file string) {
	state := app.comparison
	if app.repository == nil && app.ssh == nil || state == nil || state.result == nil ||
		app.readTask != nil && app.readTask.kind != nativeReadDiff {
		return
	}
	if file == state.file && app.readTask == nil && app.problem == "" {
		return
	}
	reader, from, target := app.comparisonReader(), state.result.From, state.result.Target
	var selected gitrepo.CommitFile
	for _, entry := range state.result.Files {
		if entry.Path == file {
			selected = entry
			break
		}
	}
	if selected.Path == "" {
		return
	}
	app.load(locales.ReadingFileDiff(), nativeReadDiff, func(ctx context.Context) (func(), error) {
		diff, err := reader.diff(ctx, from, target, selected)
		if err != nil {
			return state.restoreSelection, err
		}
		return func() {
			state.file, state.diff = file, diff
			for index, entry := range state.result.Files {
				if entry.Path == file {
					state.selected = index
					break
				}
			}
			app.clearDiff()
			app.setDiff(diff)
		}, err
	})
}

// comparisonReferences 返回比较页候选引用，按引用快照缓存在比较状态上，结果只读。
func (app *nativeApp) comparisonReferences() []string {
	if app.ssh != nil {
		return app.ssh.comparisonRefs
	}
	cache, source := &app.comparison.references, app.refSource()
	if cache.valid && cache.source == source {
		return cache.references
	}
	references := make([]string, 0, 1+len(app.repository.branches)+len(app.repository.tags))
	references = append(references, "HEAD")
	for _, branch := range app.repository.branches {
		prefix := "refs/heads/"
		if branch.Remote {
			prefix = "refs/remotes/"
		}
		references = append(references, prefix+branch.Name)
	}
	for _, tag := range app.repository.tags {
		references = append(references, "refs/tags/"+tag.Name)
	}
	*cache = nativeComparisonReferences{valid: true, source: source, references: references}
	return references
}

func (app *nativeApp) comparisonWorkspace(c *ui.Context) {
	state := app.comparison
	ui.Column(c).Fill().MinWidth(0).Children(func() {
		ui.Column(c).FillWidth().Padding(10, 12).Gap(8).Children(func() {
			references := app.comparisonReferences()
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.End).Children(func() {
				for index, label := range []string{locales.ComparisonBase(), locales.ComparisonTarget()} {
					value := &state.base
					if index == 1 {
						value = &state.target
					}
					ui.Column(c).Key(index).Grow(1).MinWidth(0).Gap(4).Children(func() {
						ui.Text(c, label).FontSize(11).TextColor(c.Theme().TextMuted)
						ui.Autocomplete(c, value, references).Label(label).Placeholder(locales.ComparisonRevisionHint()).
							FillWidth().MinWidth(0).OnSubmit(app.startComparison)
					})
					if index == 0 {
						ui.Button(c, "⇄").Label(locales.SwapComparison()).Tooltip(locales.SwapComparison()).
							OnClick(func() {
								state.base, state.target = state.target, state.base
								app.startComparison()
							})
					}
				}
			})
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				mode := 0
				if state.mergeBase {
					mode = 1
				}
				ui.Segmented(c, &mode, locales.CompareSnapshots(), locales.CompareMergeBase()).Label(locales.ComparisonMode()).
					OnChange(func() { state.mergeBase = mode == 1 })
				ui.Spacer(c)
				if app.readTask != nil {
					ui.Spinner(c).Size(14, 14)
					ui.Button(c, locales.Cancel()).OnClick(app.cancelRead)
				}
				ui.PrimaryButton(c, locales.RunComparison()).
					Disabled(strings.TrimSpace(state.base) == "" || strings.TrimSpace(state.target) == "").
					OnClick(app.startComparison)
			})
			modeHint := locales.CompareSnapshotsHint()
			if state.mergeBase {
				modeHint = locales.CompareMergeBaseHint()
			}
			ui.Text(c, modeHint).FillWidth().MinWidth(0).FontSize(11).TextColor(c.Theme().TextMuted)
		})
		ui.Divider(c)
		if state.result == nil {
			ui.Column(c).Fill().Center().Padding(20).Children(func() {
				ui.Text(c, locales.ComparisonStartHint()).TextColor(c.Theme().TextMuted)
			})
			return
		}
		mode := locales.CompareSnapshots()
		if state.resultMergeBase {
			mode = locales.CompareMergeBase()
		}
		summary := mode + " · " + strings.TrimSpace(state.resultBase) + " → " + strings.TrimSpace(state.resultTarget) +
			" (" + shortHash(state.result.From) + " → " + shortHash(state.result.Target) + ")"
		detail := state.resultBase + ": " + state.result.Base + "\n" +
			state.resultTarget + ": " + state.result.Target + "\n" + locales.ComparisonBase() + ": " + state.result.From
		ui.Text(c, summary).Label(locales.ComparisonResult()).FontSize(11).TextColor(c.Theme().TextMuted).
			FillWidth().SingleLine().Tooltip(detail).Padding(6, 12)
		ui.Divider(c)
		ui.Split(c, &app.prefs.HistoryFiles, func() {
			app.revisionFileList(c, state.result.Files, state.list, &state.selected,
				locales.ComparisonFileList(), "", app.selectComparisonFile)
		}, func() {
			if len(state.result.Files) == 0 {
				ui.Column(c).Fill().Center().Padding(20).Children(func() {
					ui.Text(c, locales.EmptyComparison()).TextColor(c.Theme().TextMuted)
				})
			} else {
				app.diffPanel(c)
			}
		}).Fill().SplitReset(280)
	})
}
