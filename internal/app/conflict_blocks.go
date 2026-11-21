// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"slices"
	"sort"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

type nativeConflictBlock struct {
	start, end             int
	oursStart, oursEnd     int
	theirsStart, theirsEnd int
	original               string
}

func parseConflictBlocks(text string, markerSize int) []nativeConflictBlock {
	var blocks []nativeConflictBlock
	var current nativeConflictBlock
	offset, depth := 0, 0
	ambiguous := false
	for line := range strings.SplitSeq(text, "\n") {
		start := offset
		offset = min(len(text), start+len(line)+1)
		marker := gitrepo.ConflictMarker(line, markerSize)
		if marker == '<' {
			depth++
			if depth == 1 {
				current = nativeConflictBlock{start: start, oursStart: offset - start}
				ambiguous = false
			} else {
				ambiguous = true
			}
			continue
		}
		if depth == 0 {
			continue
		}
		if marker == '>' {
			depth--
			if depth == 0 && !ambiguous && current.theirsStart != 0 {
				current.theirsEnd, current.end = start-current.start, offset
				blocks = append(blocks, current)
			}
			continue
		}
		if depth != 1 || ambiguous {
			continue
		}
		switch marker {
		case '|':
			if current.oursEnd != 0 || current.theirsStart != 0 {
				ambiguous = true
			}
			current.oursEnd = start - current.start
		case '=':
			if current.theirsStart != 0 {
				ambiguous = true
			}
			if current.oursEnd == 0 {
				current.oursEnd = start - current.start
			}
			current.theirsStart = offset - current.start
		}
	}
	return blocks
}

type nativeConflictBlocks struct {
	buffer     *editor.Buffer
	version    uint64
	markerSize int
	text       string
	blocks     []nativeConflictBlock
	index      int
}

func (state *nativeConflictBlocks) sync(buffer *editor.Buffer, markerSize int) {
	if buffer == nil || state.buffer == buffer && state.version == buffer.Version() && state.markerSize == markerSize {
		return
	}
	text := buffer.String()
	blocks := parseConflictBlocks(text, markerSize)
	if state.buffer == buffer {
		unresolved := blocks
		for _, choice := range state.uneditedChoices(text) {
			index := sort.Search(len(unresolved), func(index int) bool {
				return unresolved[index].end > choice.start
			})
			if index == len(unresolved) || unresolved[index].start >= choice.end {
				blocks = append(blocks, choice)
			}
		}
		slices.SortStableFunc(blocks, func(first, second nativeConflictBlock) int {
			if first.start == second.start {
				return first.end - second.end
			}
			return first.start - second.start
		})
	}
	state.buffer, state.version, state.text, state.markerSize = buffer, buffer.Version(), text, markerSize
	state.blocks, state.index = blocks, max(0, min(state.index, len(blocks)-1))
}

func (state *nativeConflictBlocks) uneditedChoices(text string) []nativeConflictBlock {
	before, after := state.text, text
	shared := min(len(before), len(after))
	prefix, suffix := 0, 0
	for prefix < shared && before[prefix] == after[prefix] {
		prefix++
	}
	for suffix < shared && before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	// 重复文本可能让编辑边界有歧义。只保留肯定在编辑区外的撤销记录。
	first, last := min(prefix, len(before)-suffix), max(prefix, len(before)-suffix)
	var choices []nativeConflictBlock
	for _, block := range state.blocks {
		if block.original == "" {
			continue
		}
		if before != after {
			switch {
			case block.end <= first && block.start < first:
			case block.start >= last && block.end > last:
				delta := len(after) - len(before)
				block.start, block.end = block.start+delta, block.end+delta
			default:
				continue
			}
		}
		if (block.start == 0 || after[block.start-1] == '\n') &&
			(strings.HasSuffix(block.original, "\n") || strings.HasSuffix(block.original, "\r") || block.end == len(after)) {
			choices = append(choices, block)
		}
	}
	return choices
}

func (state *nativeConflictBlocks) choose(incoming bool) {
	block := &state.blocks[state.index]
	start, end := block.oursStart, block.oursEnd
	if incoming {
		start, end = block.theirsStart, block.theirsEnd
	}
	raw := conflictBufferText(state.buffer)
	block.original = strings.Clone(raw[conflictRawOffset(raw, block.start):conflictRawOffset(raw, block.end)])
	selected := raw[conflictRawOffset(raw, block.start+start):conflictRawOffset(raw, block.start+end)]
	state.replace(raw, selected)
}

func (state *nativeConflictBlocks) undo() {
	block := &state.blocks[state.index]
	text := block.original
	block.original = ""
	state.replace(conflictBufferText(state.buffer), text)
}

// conflictRawOffset 将编辑器的 LF 坐标换算到保留原始换行的文本。
func conflictRawOffset(raw string, offset int) int {
	for scanned := 0; scanned < offset; {
		next := strings.IndexByte(raw[scanned:offset], '\r')
		if next < 0 {
			break
		}
		scanned += next + 1
		if scanned < len(raw) && raw[scanned] == '\n' {
			offset++
			scanned++
		}
	}
	return offset
}

func (state *nativeConflictBlocks) replace(raw, text string) {
	block := &state.blocks[state.index]
	start, end := conflictRawOffset(raw, block.start), conflictRawOffset(raw, block.end)
	expected := raw[:start] + text + raw[end:]
	state.buffer.Edit(editor.Edit{Start: block.start, End: block.end, Text: text})
	// MyGo 的 Edit 沿用目标行的结束符。只有字节不一致时才重载缓冲区，
	// 仍保留应用维护的单块撤销记录。
	if conflictBufferText(state.buffer) != expected {
		state.buffer.Reset(expected)
	}
	delta := len(text) - strings.Count(text, "\r\n") - (block.end - block.start)
	if state.buffer.Len() != len(state.text)+delta {
		state.sync(state.buffer, state.markerSize)
		return
	}
	block.end += delta
	for index := state.index + 1; index < len(state.blocks); index++ {
		next := &state.blocks[index]
		next.start, next.end = next.start+delta, next.end+delta
	}
	state.version, state.text = state.buffer.Version(), state.buffer.String()
}

func (app *nativeApp) conflictBlockToolbar(c *ui.Context) {
	state := &app.conflictBlocks
	firstView := state.buffer != app.conflictEdit.Buffer
	state.sync(app.conflictEdit.Buffer, app.conflict.MarkerSize)
	app.conflictEdit.LineBackgrounds = nil
	if len(state.blocks) == 0 {
		return
	}
	block := state.blocks[state.index]
	first := state.buffer.Pos(block.start).Line
	last := state.buffer.Pos(max(block.start, block.end-1)).Line
	app.conflictEdit.LineBackgrounds = []editor.LineBackground{
		{First: first, Last: last, Color: c.Theme().Accent.Alpha(0.12)},
	}
	reveal := func() {
		app.conflictEdit.GoTo(state.buffer.Pos(state.blocks[state.index].start))
	}
	if firstView {
		reveal()
	}
	ui.Row(c).FillWidth().Wrap().Padding(4, 12).Gap(6).AlignItems(ui.Center).
		Disabled(app.readTask != nil).Children(func() {
		previous, next := locales.PreviousHunk(), locales.NextHunk()
		ui.Button(c, "↑").Label(previous).Tooltip(previous).FontSize(12).Disabled(state.index == 0).OnClick(func() {
			state.index--
			reveal()
		})
		ui.Button(c, "↓").Label(next).Tooltip(next).FontSize(12).Disabled(state.index == len(state.blocks)-1).OnClick(func() {
			state.index++
			reveal()
		})
		ui.Text(c, locales.ConflictBlockPosition(state.index+1, len(state.blocks))).FontSize(11).TextColor(c.Theme().TextMuted)
		resolved := block.original != ""
		ui.Button(c, locales.AcceptCurrentBlock()).FontSize(12).Disabled(resolved).OnClick(func() {
			state.choose(false)
			reveal()
		})
		ui.Button(c, locales.AcceptIncomingBlock()).FontSize(12).Disabled(resolved).OnClick(func() {
			state.choose(true)
			reveal()
		})
		ui.Button(c, locales.UndoConflictBlockChoice()).FontSize(12).Disabled(!resolved).OnClick(func() {
			state.undo()
			reveal()
		})
	})
}
