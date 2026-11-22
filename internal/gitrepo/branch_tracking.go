// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

type branchOIDPair struct {
	local, upstream string
}

type branchTrackingCounts struct {
	ahead, behind int
}

// 调用方持有 repo.mu。只保留本次快照仍在使用的提交对，分支移动或消失后回收旧值。
func (repo *Repository) fillBranchTracking(ctx context.Context, branches []Branch, records [][]string, oids map[string]string, replacements bool) error {
	const oidFormat = "%(refname)%00%(objectname)"
	var extra []string
	for index, fields := range records {
		upstream := fields[7]
		if !branches[index].Remote && upstream != "" && oids[upstream] == "" &&
			!strings.HasPrefix(upstream, "refs/heads/") && !strings.HasPrefix(upstream, "refs/remotes/") {
			extra = append(extra, upstream)
		}
	}
	if err := repo.readTrackingRefs(ctx, oidFormat, 2, extra, func(fields []string) {
		oids[fields[0]] = fields[1]
	}); err != nil {
		return err
	}

	next := make(map[branchOIDPair]branchTrackingCounts, len(repo.branchTracking))
	var missing []string
	indices := make(map[string]int)
	cacheable := !replacements && repo.branchHistoryCacheable()
	for index, fields := range records {
		upstream := fields[7]
		if branches[index].Remote || oids[upstream] == "" {
			continue
		}
		pair := branchOIDPair{local: fields[8], upstream: oids[upstream]}
		if counts, ok := repo.branchTracking[pair]; cacheable && ok {
			branches[index].Ahead, branches[index].Behind = counts.ahead, counts.behind
			next[pair] = counts
			continue
		}
		missing = append(missing, fields[0])
		indices[fields[0]] = index
	}
	if len(missing) > 0 {
		type trackedBranch struct {
			pair     branchOIDPair
			upstream string
			counts   branchTrackingCounts
		}
		calculated := make([]trackedBranch, 0, len(missing))
		var check []string
		verified := make(map[string]string)
		err := repo.readTrackingRefs(ctx, oidFormat+"%00%(upstream)%00%(upstream:track,nobracket)", 4, missing, func(fields []string) {
			index, ok := indices[fields[0]]
			upstream := fields[2]
			if !ok || fields[1] != records[index][8] || upstream != records[index][7] {
				return
			}
			ahead, behind := branchDivergence(fields[3])
			branches[index].Ahead, branches[index].Behind = ahead, behind
			if cacheable && fields[3] != "gone" {
				calculated = append(calculated, trackedBranch{
					pair:     branchOIDPair{local: fields[1], upstream: oids[upstream]},
					upstream: upstream, counts: branchTrackingCounts{ahead: ahead, behind: behind},
				})
				if _, exists := verified[upstream]; !exists {
					check = append(check, upstream)
					verified[upstream] = ""
				}
			}
		})
		if err != nil {
			return err
		}
		// Git 计算计数时会再次解析上游，外部 fetch 并不持有 mu。
		// 上游在计算前后保持同一对象 ID 才能将结果用于后续刷新。
		if err := repo.readTrackingRefs(ctx, oidFormat, 2, check, func(fields []string) {
			verified[fields[0]] = fields[1]
		}); err != nil {
			return err
		}
		for _, item := range calculated {
			if verified[item.upstream] == item.pair.upstream {
				next[item.pair] = item.counts
			}
		}
	}
	repo.branchTracking = next
	return nil
}

func (repo *Repository) readTrackingRefs(ctx context.Context, format string, width int, names []string, record func([]string)) error {
	// 分批限制参数长度，兼容没有 for-each-ref --stdin 的 Git。
	for len(names) > 0 {
		size, count := len(names[0])+1, 1
		for count < len(names) && size+len(names[count])+1 <= 8<<10 {
			size += len(names[count]) + 1
			count++
		}
		args := append([]string{"for-each-ref", "--format=" + format, "--"}, names[:count]...)
		output, err := repo.git(ctx, args...)
		if err != nil {
			return err
		}
		records, err := refRecords(output, width, "无法解析分支同步状态")
		if err != nil {
			return err
		}
		for _, fields := range records {
			record(fields)
		}
		names = names[count:]
	}
	return nil
}

func (repo *Repository) branchHistoryCacheable() bool {
	directories, ok := localMetadataDirectories(repo.Path)
	if !ok {
		return false
	}
	// 浅克隆和 grafts 可以在两个对象 ID 不变时改变可达历史。
	for _, name := range []string{"shallow", "info/grafts"} {
		if _, err := os.Lstat(filepath.Join(directories[1], name)); !os.IsNotExist(err) {
			return false
		}
	}
	return true
}
