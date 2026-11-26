// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui/editor"
)

type nativeReviewNotes map[string][]reviewNote

type reviewNote struct {
	excerpts []reviewExcerpt
	comment  string
}

type reviewExcerpt struct {
	path        string
	side        editor.DiffSide
	first, last int
	code        string
}

func newReviewExcerpt(patch editor.FilePatch, side editor.DiffSide, first int, text editor.Snapshot, start, end int) reviewExcerpt {
	path := patch.NewName
	if side == editor.DiffOld {
		path = patch.OldName
	}
	return reviewExcerpt{
		path: path, side: side, first: first, last: first + end - start,
		code: text.Slice(text.LineStart(start), text.LineEnd(end)),
	}
}

func (diff *nativeUnifiedDiff) splitReviewSelection(state *editor.DiffState, lines editor.DiffLineRange) []reviewExcerpt {
	buffer := state.New
	if lines.Side == editor.DiffOld {
		buffer = state.Old
	}
	if buffer == nil || lines.Start < 0 || lines.End < lines.Start {
		return nil
	}
	text := buffer.Snapshot()
	if lines.End >= text.LineCount() {
		return nil
	}
	if diff.context != nil {
		if lines.End == text.LineCount()-1 && text.LineStart(lines.End) == text.Len() {
			lines.End--
		}
		if lines.End < lines.Start {
			return nil
		}
		return []reviewExcerpt{newReviewExcerpt(diff.patch, lines.Side, lines.Start+1, text, lines.Start, lines.End)}
	}

	// SetPatch 的缓冲区紧邻拼接各 hunk，但两个 hunk 之间可能略去了许多源文件行。
	var excerpts []reviewExcerpt
	offset := 0
	for _, hunk := range diff.patch.Hunks {
		first, count := hunk.NewStart, hunk.NewLines
		if lines.Side == editor.DiffOld {
			first, count = hunk.OldStart, hunk.OldLines
		}
		start, end := max(offset, lines.Start), min(offset+count-1, lines.End)
		if start <= end {
			excerpts = append(excerpts, newReviewExcerpt(diff.patch, lines.Side, first+start-offset, text, start, end))
		}
		offset += count
		if offset > lines.End {
			break
		}
	}
	return excerpts
}

func (excerpt reviewExcerpt) location() string {
	side := locales.ReviewNoteNewSide()
	if excerpt.side == editor.DiffOld {
		side = locales.ReviewNoteOldSide()
	}
	location := fmt.Sprintf("%s:L%d", excerpt.path, excerpt.first)
	if excerpt.last != excerpt.first {
		location += fmt.Sprintf("-L%d", excerpt.last)
	}
	return location + " (" + side + ")"
}

func reviewDelimiter(text string, minimum int) string {
	longest, run := minimum-1, 0
	for _, char := range text {
		if char == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", longest+1)
}

func (notes nativeReviewNotes) markdown(repo string) string {
	var output strings.Builder
	for index, note := range notes[repo] {
		if index > 0 {
			output.WriteString("\n\n")
		}
		for _, excerpt := range note.excerpts {
			location := excerpt.location()
			if strings.ContainsAny(location, "\n\r\t") {
				location = strconv.Quote(location)
			}
			inline := reviewDelimiter(location, 1)
			fence := reviewDelimiter(excerpt.code, 3)
			fmt.Fprintf(&output, "### %s %s %s\n\n%s\n%s\n%s\n\n", inline, location, inline, fence, excerpt.code, fence)
		}
		output.WriteString(note.comment)
	}
	return output.String()
}
