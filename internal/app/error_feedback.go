// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"regexp"
	"strings"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/plugins/sfsymbols"
	"github.com/egoist/mygo/ui"
)

type nativeProblem struct {
	title   string
	summary string
	hint    string
	raw     string
}

var branchWorktreeProblem = regexp.MustCompile(`cannot delete branch '([^']+)' (?:used by worktree at|checked out at) '[^']+'`)

func describeNativeProblem(raw string) nativeProblem {
	problem := nativeProblem{title: locales.OperationFailed(), raw: raw}
	first, _, _ := strings.Cut(strings.TrimSpace(raw), "\n")
	problem.summary = first
	lower := strings.ToLower(raw)
	switch {
	case branchWorktreeProblem.MatchString(raw):
		match := branchWorktreeProblem.FindStringSubmatch(raw)
		problem.title = locales.BranchDeleteFailed()
		problem.summary = locales.BranchInWorktree(match[1])
		problem.hint = locales.BranchInWorktreeHint()
	case strings.Contains(lower, "not fully merged"):
		problem.title = locales.BranchDeleteFailed()
		problem.summary = locales.BranchUnmergedError()
		problem.hint = locales.BranchUnmergedHint()
	case strings.Contains(lower, "conflict ("), strings.Contains(lower, "unmerged files"),
		strings.Contains(lower, "automatic merge failed"):
		problem.title = locales.MergeConflictError()
		problem.summary = locales.MergeConflictReason()
		problem.hint = locales.ResolveAllConflicts()
	case strings.Contains(lower, "authentication failed"), strings.Contains(lower, "permission denied (publickey"):
		problem.title = locales.AuthenticationError()
		problem.summary = locales.AuthenticationErrorReason()
		problem.hint = locales.AuthenticationErrorHint()
	case strings.Contains(lower, "could not resolve host"), strings.Contains(lower, "failed to connect"),
		strings.Contains(lower, "connection timed out"), strings.Contains(lower, "connection refused"):
		problem.title = locales.ConnectionError()
		problem.summary = locales.ConnectionErrorReason()
		problem.hint = locales.ConnectionErrorHint()
	case strings.Contains(lower, "permission denied"):
		problem.title = locales.PermissionError()
		problem.summary = locales.PermissionErrorReason()
		problem.hint = locales.PermissionErrorHint()
	default:
		for _, prefix := range []string{"fatal:", "error:", "ssh:", "remote:"} {
			if strings.HasPrefix(strings.ToLower(first), prefix) {
				problem.summary = locales.ErrorSummaryFallback()
				break
			}
		}
	}
	return problem
}

func problemIcon(c *ui.Context, size float32) {
	sfsymbols.Symbol(c, "exclamationmark.circle").Size(size).Color(c.Theme().Danger).Role(ui.RoleNone)
}

func (app *nativeApp) problemNotice(c *ui.Context) {
	if app.busy || app.problem == "" || app.dialog != nil && app.dialog.errorDetails != nil {
		return
	}
	problem := describeNativeProblem(app.problem)
	ui.Divider(c)
	ui.Row(c).FillWidth().MinHeight(48).Shrink(0).Padding(8, 14).Gap(10).AlignItems(ui.Center).
		Background(c.Theme().Background.Mix(c.Theme().Danger, 0.06)).Children(func() {
		problemIcon(c, 18)
		ui.Text(c, problem.title).FontSize(13).Bold().MaxLines(1).Shrink(0)
		ui.Text(c, problem.summary).FontSize(12).Grow(1).MinWidth(0).MaxLines(1).Selectable().Tooltip(problem.raw)
		ui.Button(c, locales.ShowDetails()).Shrink(0).Disabled(app.dialog != nil).
			OnClick(func() { app.showProblemDetails(problem.raw) })
		if iconButton(c, locales.DismissNotice(), nativeIcons["close"]).Shrink(0).Clicked() {
			app.problem = ""
		}
	})
}

func (app *nativeApp) inlineProblem(c *ui.Context, raw string) ui.Element {
	problem := describeNativeProblem(raw)
	return ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		problemIcon(c, 16)
		ui.Text(c, problem.summary).FontSize(12).TextColor(c.Theme().Danger).
			Grow(1).MinWidth(0).MaxLines(3).Selectable().Tooltip(raw)
		if problem.summary != raw || strings.Contains(raw, "\n") || len([]rune(raw)) > 160 {
			ui.Button(c, locales.ShowDetails()).FontSize(12).Shrink(0).
				OnClick(func() { app.showProblemDetails(raw) })
		}
	})
}

func (app *nativeApp) showProblemDetails(raw string) {
	if app.busy || app.closed {
		return
	}
	parent := app.dialog
	if parent != nil && parent.errorDetails != nil {
		if parent.errorDetails.raw == raw {
			return
		}
		parent = parent.parent
	}
	problem := describeNativeProblem(raw)
	app.dialog = &nativeDialog{
		parent: parent, title: problem.title, width: 600, errorDetails: &problem,
	}
}

func (app *nativeApp) problemDetailsView(c *ui.Context, dialog *nativeDialog) {
	problem := dialog.errorDetails
	if problem.summary != strings.TrimSpace(problem.raw) {
		ui.Text(c, problem.summary).FontSize(13).FillWidth().MaxLines(4).Selectable().Tooltip(problem.summary)
	}
	if problem.hint != "" {
		ui.Text(c, problem.hint).FontSize(12).TextColor(c.Theme().TextMuted).FillWidth().MaxLines(4).Selectable()
	}
	_, height := c.Size()
	ui.Scroll(c).Label(locales.RawErrorDetails()).FillWidth().MaxHeight(min(220, max(64, height-280))).
		Background(c.Theme().Surface).Radius(6).Children(func() {
		ui.Text(c, problem.raw).Font("monospace").FontSize(12).FillWidth().Padding(10).Selectable()
	})
	services := c.Services()
	ui.Row(c).FillWidth().Gap(8).Padding(8, 0, 0, 0).Justify(ui.End).Children(func() {
		ui.Button(c, locales.CopyErrorDetails()).OnClick(func() { services.WriteClipboard(problem.raw) })
		ui.PrimaryButton(c, locales.Close()).AutoFocus().OnClick(func() { app.closeDialog(dialog) })
	})
}
