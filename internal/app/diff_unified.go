// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

type nativeUnifiedDiff struct {
	patch          editor.FilePatch
	context        *editor.Buffer
	state          editor.State
	rows           []nativeUnifiedRow
	hunkRows       []int
	pending        int
	backgroundRows int
	added, deleted ui.Color
}

type nativeUnifiedRow struct {
	old, new int
	kind     byte
	gap      int
	note     string
}

func (diff *nativeUnifiedDiff) build(name string) {
	var text strings.Builder
	old, newLine := 1, 1
	add := func(code string, row nativeUnifiedRow) {
		if len(diff.rows) > 0 {
			text.WriteByte('\n')
		}
		text.WriteString(code)
		diff.rows = append(diff.rows, row)
	}
	for index, hunk := range diff.patch.Hunks {
		oldStart, newStart := hunk.OldStart, hunk.NewStart
		if hunk.OldLines == 0 {
			oldStart++
		}
		if hunk.NewLines == 0 {
			newStart++
		}
		if diff.context != nil && oldStart > old {
			add("", nativeUnifiedRow{old: old, new: newLine, gap: oldStart - old})
		} else if diff.context == nil && (index > 0 || oldStart > 1 || newStart > 1) {
			note := fmt.Sprintf("@@ -%d,%d +%d,%d @@", hunk.OldStart, hunk.OldLines, hunk.NewStart, hunk.NewLines)
			add("", nativeUnifiedRow{note: note})
		}
		old, newLine = oldStart, newStart
		diff.hunkRows = append(diff.hunkRows, len(diff.rows))
		for _, line := range hunk.Lines {
			if line == "" {
				continue
			}
			switch line[0] {
			case '-':
				add(strings.TrimSuffix(line[1:], "\r"), nativeUnifiedRow{old: old, kind: '-'})
				old++
			case '+':
				add(strings.TrimSuffix(line[1:], "\r"), nativeUnifiedRow{new: newLine, kind: '+'})
				newLine++
			case ' ':
				add(strings.TrimSuffix(line[1:], "\r"), nativeUnifiedRow{old: old, new: newLine})
				old++
				newLine++
			case '\\':
				add("", nativeUnifiedRow{note: line})
			}
		}
	}
	if diff.context != nil {
		source := diff.context.Snapshot()
		lineCount := source.LineCount()
		if source.Len() == 0 || source.Line(lineCount-1) == "" {
			lineCount--
		}
		if lineCount >= old {
			add("", nativeUnifiedRow{old: old, new: newLine, gap: lineCount - old + 1})
		}
	}
	buffer := editor.NewBuffer(text.String())
	buffer.SetLanguage(editor.LanguageFor(name))
	diff.state = editor.State{
		Buffer: buffer, Label: locales.DiffText(), ReadOnly: true,
		LineNumbers: editor.LineNumbersOff, Whitespace: editor.WhitespaceNone,
		NoFolding: true, NoActiveLine: true, NoIndentGuides: true, NoScrollPastEnd: true,
	}
}

func (diff *nativeUnifiedDiff) goToHunk(index int) {
	diff.pending = index + 1
}

func (diff *nativeUnifiedDiff) expand(line int, all bool) {
	row := diff.rows[line]
	count := min(100, row.gap)
	if all {
		count = row.gap
	}
	source := diff.context.Snapshot()
	rows := make([]nativeUnifiedRow, 0, count+1)
	var text strings.Builder
	for offset := range count {
		if offset > 0 {
			text.WriteByte('\n')
		}
		text.WriteString(source.Line(row.old + offset - 1))
		rows = append(rows, nativeUnifiedRow{old: row.old + offset, new: row.new + offset})
	}
	if row.gap > count {
		text.WriteByte('\n')
		rows = append(rows, nativeUnifiedRow{old: row.old + count, new: row.new + count, gap: row.gap - count})
	}
	for index, start := range diff.hunkRows {
		if start > line {
			diff.hunkRows[index] += len(rows) - 1
		}
	}
	snapshot := diff.state.Buffer.Snapshot()
	offset := snapshot.LineStart(line)
	diff.state.Buffer.Edit(editor.Edit{Start: offset, End: offset, Text: text.String()})
	diff.rows = append(append(diff.rows[:line:line], rows...), diff.rows[line+1:]...)
	diff.backgroundRows = 0
}

func (diff *nativeUnifiedDiff) view(c *ui.Context, name string, wrap, fit bool) ui.Element {
	if diff.state.Buffer == nil {
		diff.build(name)
	}
	state := &diff.state
	state.SoftWrap, state.FitContent = wrap, fit
	if diff.pending > 0 && diff.pending <= len(diff.hunkRows) {
		state.GoTo(editor.Pos{Line: diff.hunkRows[diff.pending-1]})
		diff.pending = 0
	}
	theme := editor.DefaultTheme(c.Theme())
	diff.updateBackgrounds(theme)
	root := ui.Row(c).Clip()
	root.Children(func() {
		ui.Box(c).Width(72).Shrink(0).AlignSelf(ui.Stretch).HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind == ui.InputScroll && !fit {
				before := state.ScrollY()
				state.SetScrollY(before + float64(ev.DY))
				return state.ScrollY() != before
			}
			if ev.Kind != ui.InputPointerDown || ev.Button != 0 {
				return false
			}
			top := state.ScrollY() + float64(ev.Y)
			line := sort.Search(len(diff.rows), func(index int) bool { return state.LineTop(index+1) > top })
			if line < len(diff.rows) && diff.rows[line].gap > 0 {
				diff.expand(line, ev.Mods&ui.Shift != 0)
				return true
			}
			return false
		})
		text := editor.View(c, state).Grow(1).MinWidth(0)
		if !fit {
			text.FillHeight()
		}
	})
	root.DrawOver(func(p *ui.Painter, bounds ui.Rect) {
		scroll := state.ScrollY()
		visible := p.Visible()
		start := sort.Search(len(diff.rows), func(index int) bool {
			return state.LineTop(index+1)-scroll >= float64(visible.Y-bounds.Y)
		})
		for line := start; line < len(diff.rows); line++ {
			top := bounds.Y + float32(state.LineTop(line)-scroll)
			if top >= visible.Y+visible.H {
				break
			}
			row := diff.rows[line]
			if row.gap > 0 {
				p.Text(bounds.X+8, top+2, fmt.Sprintf("↕ %d", row.gap), 12, theme.LineNumber)
				continue
			}
			if row.note != "" {
				p.Text(bounds.X+80, top+2, row.note, 12, theme.LineNumber)
				continue
			}
			if row.old > 0 {
				p.Text(bounds.X+4, top+2, strconv.Itoa(row.old), 12, theme.LineNumber)
			}
			if row.new > 0 {
				p.Text(bounds.X+31, top+2, strconv.Itoa(row.new), 12, theme.LineNumber)
			}
			switch row.kind {
			case '-':
				p.Text(bounds.X+58, top+2, "−", 13, theme.Deleted)
			case '+':
				p.Text(bounds.X+58, top+2, "+", 13, theme.Added)
			}
		}
	})
	return root
}

func (diff *nativeUnifiedDiff) updateBackgrounds(theme *editor.Theme) {
	if diff.backgroundRows == len(diff.rows) && diff.added == theme.Added && diff.deleted == theme.Deleted {
		return
	}
	diff.backgroundRows, diff.added, diff.deleted = len(diff.rows), theme.Added, theme.Deleted
	backgrounds := diff.state.LineBackgrounds[:0]
	for line, row := range diff.rows {
		if row.kind == 0 {
			continue
		}
		if last := len(backgrounds) - 1; last >= 0 && backgrounds[last].Last == line-1 && diff.rows[line-1].kind == row.kind {
			backgrounds[last].Last = line
			continue
		}
		color := theme.Added
		if row.kind == '-' {
			color = theme.Deleted
		}
		backgrounds = append(backgrounds, editor.LineBackground{First: line, Last: line, Color: color.Alpha(0.12)})
	}
	diff.state.LineBackgrounds = backgrounds
	diff.state.TextBackgrounds = diff.inlineBackgrounds(theme)
}

func (diff *nativeUnifiedDiff) inlineBackgrounds(theme *editor.Theme) []editor.TextBackground {
	source := diff.state.Buffer.Snapshot()
	var highlights []editor.TextBackground
	for line := 0; line < len(diff.rows); {
		if diff.rows[line].kind != '-' {
			line++
			continue
		}
		oldStart := line
		for line < len(diff.rows) && diff.rows[line].kind == '-' {
			line++
		}
		newStart := line
		for line < len(diff.rows) && diff.rows[line].kind == '+' {
			line++
		}
		if newStart-oldStart != line-newStart {
			continue
		}
		for offset := range newStart - oldStart {
			oldLine, newLine := oldStart+offset, newStart+offset
			before, after := source.Line(oldLine), source.Line(newLine)
			prefix, suffix := commonDiffEdges(before, after)
			for index, line := range [2]int{oldLine, newLine} {
				length := len([2]string{before, after}[index]) - prefix - suffix
				if length > 0 {
					start := source.LineStart(line) + prefix
					color := [2]ui.Color{theme.Deleted, theme.Added}[index]
					highlights = append(highlights, editor.TextBackground{Start: start, End: start + length, Color: color.Alpha(0.24)})
				}
			}
		}
	}
	return highlights
}

func commonDiffEdges(before, after string) (prefix, suffix int) {
	for prefix < min(len(before), len(after)) && before[prefix] == after[prefix] {
		prefix++
	}
	for prefix > 0 && prefix < len(before) && !utf8.RuneStart(before[prefix]) {
		prefix--
	}
	for suffix < min(len(before), len(after))-prefix && before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	for suffix > 0 && !utf8.RuneStart(before[len(before)-suffix]) {
		suffix--
	}
	return
}
