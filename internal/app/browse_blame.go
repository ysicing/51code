// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"strings"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

type nativeBrowserLineBlame struct {
	source *gitrepo.FileContent
	buffer *editor.Buffer
	line   int
	cancel context.CancelFunc
	open   bool
	ready  bool
	info   gitrepo.LineBlame
	hint   string
}

func (state *nativeBrowser) cancelBlame() {
	if blame := state.blame; blame != nil && !blame.ready {
		blame.cancel()
		state.blame = nil
	}
}

func (state *nativeBrowser) currentBlame(buffer *editor.Buffer, line int) *nativeBrowserLineBlame {
	if blame := state.blame; blame != nil && blame.buffer == buffer && blame.line == line &&
		blame.source == state.viewer.sources[state.viewer.side] {
		return blame
	}
	return nil
}

func browserBlameLine(viewer *editor.State) (int, bool) {
	line := viewer.CursorPos().Line
	snapshot := viewer.Buffer.Snapshot()
	return line, line >= 0 && line < snapshot.LineCount() && snapshot.LineStart(line) < snapshot.Len()
}

func (app *nativeApp) updateBrowserBlame(state *nativeBrowser, viewer *editor.State) {
	var visible *nativeBrowserLineBlame
	if line, ok := browserBlameLine(viewer); ok && viewer.Focused() && viewer.Selection().Empty() {
		blame := state.currentBlame(viewer.Buffer, line)
		if blame != nil && blame.ready {
			if blame.hint != "" {
				visible = blame
			}
		} else if blame == nil && app.readTask == nil {
			app.loadBrowserBlame(state, viewer.Buffer, line, false)
		}
	}
	if state.shownBlame == visible {
		return
	}
	state.shownBlame = visible
	if visible == nil {
		viewer.SetInlays(nil)
		return
	}
	viewer.SetInlays([]editor.Inlay{{
		Offset: viewer.Buffer.Snapshot().LineEnd(visible.line), Text: "  " + visible.hint, Trailing: true, Ellipsis: "…",
	}})
}

func (app *nativeApp) browserBlameMenu(state *nativeBrowser, viewer *editor.State, menu *ui.Menu) {
	line, ok := browserBlameLine(viewer)
	if !ok {
		return
	}
	blame := state.currentBlame(viewer.Buffer, line)
	if !menu.Item(locales.ViewLineCommit()).Disabled(app.busy || app.dialog != nil ||
		app.readTask != nil || blame != nil && blame.ready && blame.info.Hash == "" && blame.hint != "").Chosen() {
		return
	}
	if blame != nil && blame.ready && blame.info.Hash != "" {
		app.revealHistoryCommit(blame.info.Hash)
		return
	}
	if blame != nil && !blame.ready {
		blame.open = true
		return
	}
	app.loadBrowserBlame(state, viewer.Buffer, line, true)
}

func (app *nativeApp) loadBrowserBlame(state *nativeBrowser, buffer *editor.Buffer, line int, openCommit bool) {
	if app.repository == nil && app.ssh == nil || app.busy || app.closed || app.dialog != nil || app.problem != "" && !openCommit {
		return
	}
	state.cancelBlame()
	read, post := app.browserSource().blame, app.post
	source := state.viewer.sources[state.viewer.side]
	ctx, cancel := context.WithCancel(app.ctx)
	blame := &nativeBrowserLineBlame{source: source, buffer: buffer, line: line, cancel: cancel, open: openCommit}
	state.blame = blame
	go func() {
		defer cancel()
		if !openCommit {
			timer := time.NewTimer(90 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
		info, err := read(ctx, source, line+1)
		if ctx.Err() != nil {
			return
		}
		post(func() {
			if app.closed || app.screen != "files" || app.browser != state ||
				state.blame != blame || state.viewer.sources[state.viewer.side] != source {
				return
			}
			blame.ready, blame.info = true, info
			if err == nil {
				blame.hint = browserBlameHint(info, time.Now())
			}
			if blame.open {
				if err != nil {
					app.problem = err.Error()
					app.showProblemDetails(app.problem)
				} else if info.Hash != "" {
					app.revealHistoryCommit(info.Hash)
				}
			}
		})
	}()
}

func browserBlameHint(info gitrepo.LineBlame, now time.Time) string {
	if info.Hash == "" {
		return locales.UncommittedChanges()
	}
	age := now.Sub(info.AuthoredAt)
	days := int(age / (24 * time.Hour))
	parts := []string{info.Author}
	if !info.AuthoredAt.IsZero() {
		switch {
		case days >= 365:
			parts = append(parts, locales.BlameYearsAgo(days/365))
		case days >= 30:
			parts = append(parts, locales.BlameMonthsAgo(days/30))
		case days >= 7:
			parts = append(parts, locales.BlameWeeksAgo(days/7))
		case days > 0:
			parts = append(parts, locales.BlameDaysAgo(days))
		case age >= time.Hour:
			parts = append(parts, locales.BlameHoursAgo(int(age/time.Hour)))
		case age >= time.Minute:
			parts = append(parts, locales.BlameMinutesAgo(int(age/time.Minute)))
		default:
			parts = append(parts, locales.BlameJustNow())
		}
	}
	if info.Subject != "" {
		parts = append(parts, info.Subject)
	}
	return strings.Join(parts, " · ")
}
