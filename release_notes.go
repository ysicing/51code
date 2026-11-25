// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package main

import "strings"

type releaseNoteGroups struct {
	items map[string][]string
	bytes int
}

func releaseCategory(heading string) string {
	switch heading {
	case "新功能", "新增", "新增功能":
		return "新增功能"
	case "改进", "功能改进":
		return "功能改进"
	case "修复", "问题修复":
		return "问题修复"
	case "废弃", "弃用", "已废弃":
		return "已废弃"
	case "移除", "已移除":
		return "已移除"
	default:
		return "其他更新"
	}
}

func (groups *releaseNoteGroups) add(version, notes string) bool {
	category := "其他更新"
	prefix := ""
	var lines []string
	flush := func() bool {
		if len(lines) == 0 {
			return true
		}
		text := strings.Join(lines, "\n")
		size := len("- ") + len(prefix) + len(text) + len(" `v`\n") + len(version)
		if groups.bytes+size > maxUpdateManifest {
			return false
		}
		item := "- " + prefix + text + " `v" + version + "`"
		groups.items[category] = append(groups.items[category], item)
		groups.bytes += size
		lines = lines[:0]
		return true
	}
	for line := range strings.SplitSeq(strings.ReplaceAll(notes, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		headingLine := strings.HasPrefix(line, "### ")
		bullet := strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ")
		if (line == "" || headingLine || bullet) && !flush() {
			return false
		}
		switch {
		case line == "":
			continue
		case headingLine:
			heading := strings.TrimSpace(strings.TrimPrefix(line, "### "))
			category = releaseCategory(heading)
			prefix = ""
			if category == "其他更新" && heading != category {
				prefix = heading + "："
			}
		case bullet:
			lines = append(lines, strings.TrimSpace(line[2:]))
		default:
			lines = append(lines, line)
		}
	}
	return flush()
}

func (groups releaseNoteGroups) markdown() string {
	var sections []string
	for _, heading := range []string{"新增功能", "功能改进", "问题修复", "已废弃", "已移除", "其他更新"} {
		if items := groups.items[heading]; len(items) > 0 {
			sections = append(sections, "## "+heading+"\n\n"+strings.Join(items, "\n"))
		}
	}
	return strings.Join(sections, "\n\n")
}
