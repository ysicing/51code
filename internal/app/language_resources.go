// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

type languageSystemSample struct {
	cpuPercent      float64
	memoryTotal     uint64
	memoryAvailable uint64
}

type languageResourceMonitor struct {
	sample     func(context.Context) (languageSystemSample, error)
	interval   time.Duration
	maxWait    time.Duration
	readyUntil time.Time
}

func readLanguageSystemResources(ctx context.Context) (languageSystemSample, error) {
	// 每次独立采样，避免零间隔 API 的全局基线或长时间空闲掩盖当前负载。
	usage, err := cpu.PercentWithContext(ctx, 500*time.Millisecond, false)
	if err != nil {
		return languageSystemSample{}, err
	}
	if len(usage) != 1 {
		return languageSystemSample{}, fmt.Errorf("无法读取系统 CPU 利用率：返回 %d 项", len(usage))
	}
	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return languageSystemSample{}, err
	}
	return languageSystemSample{
		cpuPercent: usage[0], memoryTotal: memory.Total, memoryAvailable: memory.Available,
	}, ctx.Err()
}

func languageResourceWaiter() func(context.Context) error {
	monitor := &languageResourceMonitor{sample: readLanguageSystemResources, interval: 5 * time.Second}
	return monitor.wait
}

func languageResourcesAvailable(sample languageSystemSample) bool {
	// Available 在不同平台的回收口径不同，只有比例和绝对余量同时偏低才阻止有界扫描。
	return sample.cpuPercent >= 0 && sample.cpuPercent < 85 &&
		sample.memoryTotal > 0 && sample.memoryAvailable <= sample.memoryTotal &&
		(sample.memoryAvailable > 512<<20 || float64(sample.memoryAvailable)/float64(sample.memoryTotal) > 0.05)
}

func (monitor *languageResourceMonitor) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if time.Now().Before(monitor.readyUntil) {
		return nil
	}
	maxWait := monitor.maxWait
	if maxWait <= 0 {
		maxWait = 30 * time.Second
	}
	// 资源长期不足时结束本轮，保留缓存，避免持续采样直到窗口关闭。
	ctx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()
	interval := monitor.interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := monitor.sample(ctx)
		if err != nil {
			return err
		}
		if languageResourcesAvailable(current) {
			monitor.readyUntil = time.Now().Add(interval)
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
