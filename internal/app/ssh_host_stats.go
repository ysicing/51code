// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

type sshStatusSample struct {
	gitrepo.SSHHostStats
	at time.Time
}

func (app *nativeApp) startSSHStats() {
	state := app.ssh
	if state == nil || state.stopStats != nil || app.closed {
		return
	}
	ctx, cancel := context.WithCancel(app.ctx)
	state.stopStats = cancel
	repo, post := state.repo, app.post
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		var previous sshStatusSample
		for ctx.Err() == nil {
			sample := sshStatusSample{SSHHostStats: repo.HostStats(ctx), at: time.Now()}
			down, up := sample.rates(previous)
			previous = sample
			if ctx.Err() != nil {
				return
			}
			post(func() {
				if ctx.Err() == nil && !app.closed && app.ssh == state {
					state.stats, state.down, state.up = sample.SSHHostStats, down, up
				}
			})
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (app *nativeApp) stopSSHStats() {
	if state := app.ssh; state != nil && state.stopStats != nil {
		state.stopStats()
		state.stopStats = nil
	}
}

func (sample sshStatusSample) rates(previous sshStatusSample) (string, string) {
	seconds := sample.at.Sub(previous.at).Seconds()
	if sample.Interface == "" || sample.Interface != previous.Interface || seconds <= 0 ||
		sample.Received < previous.Received || sample.Transmitted < previous.Transmitted {
		return "", ""
	}
	return sshByteRate(float64(sample.Received-previous.Received) / seconds),
		sshByteRate(float64(sample.Transmitted-previous.Transmitted) / seconds)
}

func sshByteRate(rate float64) string {
	for _, unit := range []string{"B/s", "KB/s", "MB/s", "GB/s"} {
		if rate < 1000 || unit == "GB/s" {
			if unit == "B/s" || rate >= 10 {
				return fmt.Sprintf("%.0f %s", rate, unit)
			}
			return fmt.Sprintf("%.1f %s", rate, unit)
		}
		rate /= 1000
	}
	return ""
}

func (state *nativeSSHWorkspace) hostStatus(c *ui.Context) {
	stats := state.stats
	load, averages, cpus := "—", "—", "—"
	if stats.HasLoad {
		load = fmt.Sprintf("%.2f", stats.Load[0])
		averages = fmt.Sprintf("%s / %.2f / %.2f", load, stats.Load[1], stats.Load[2])
	}
	if stats.CPUs > 0 {
		cpus = strconv.Itoa(stats.CPUs)
	}
	down, up := state.down, state.up
	if down == "" {
		down, up = "—", "—"
	}
	iface := stats.Interface
	if iface == "" {
		iface = "—"
	}
	ui.Text(c, locales.SshHostLoad(load)).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine().Shrink(0).
		Tooltip(locales.SshLoadDetails(averages, cpus))
	ui.Text(c, "↓ "+down+"   ↑ "+up).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine().Shrink(0).
		Tooltip(locales.SshNetworkDetails(iface))
}
