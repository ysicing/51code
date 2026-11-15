// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"
)

func TestLanguageResourceAdmission(t *testing.T) {
	for _, test := range []struct {
		name      string
		cpu       float64
		total     uint64
		available uint64
		ready     bool
	}{
		{name: "idle", cpu: 0, total: 1000, available: 500, ready: true},
		{name: "busy", cpu: 90, total: 1000, available: 500},
		{name: "CPU threshold", cpu: 85, total: 1000, available: 500},
		{name: "below CPU threshold", cpu: 84.99, total: 1000, available: 500, ready: true},
		{name: "low available memory", cpu: 10, total: 1000, available: 49},
		{name: "memory threshold", cpu: 10, total: 1000, available: 50},
		{name: "above memory threshold", cpu: 10, total: 1000, available: 51, ready: true},
		{name: "below old memory threshold", cpu: 10, total: 1000, available: 99, ready: true},
		{name: "large machine with low ratio", cpu: 10, total: 64 << 30, available: 1 << 30, ready: true},
		{name: "absolute memory threshold", cpu: 10, total: 16 << 30, available: 512 << 20},
		{name: "above absolute memory threshold", cpu: 10, total: 16 << 30, available: (512 << 20) + 1, ready: true},
		{name: "small machine with sufficient ratio", cpu: 10, total: 1 << 30, available: 100 << 20, ready: true},
		{name: "no available memory", total: 1000},
		{name: "unknown memory capacity"},
		{name: "invalid memory capacity", total: 1000, available: 1001},
		{name: "negative CPU", cpu: -1, total: 1000, available: 500},
		{name: "invalid CPU", cpu: math.NaN(), total: 1000, available: 500},
		{name: "infinite CPU", cpu: math.Inf(1), total: 1000, available: 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			sample := languageSystemSample{
				cpuPercent: test.cpu, memoryTotal: test.total, memoryAvailable: test.available,
			}
			if got := languageResourcesAvailable(sample); got != test.ready {
				t.Fatalf("resource admission = %v, want %v", got, test.ready)
			}
		})
	}
}

func TestLanguageResourceWaitHasDeadline(t *testing.T) {
	monitor := &languageResourceMonitor{
		interval: time.Hour,
		maxWait:  20 * time.Millisecond,
		sample: func(context.Context) (languageSystemSample, error) {
			return languageSystemSample{cpuPercent: 10, memoryTotal: 16 << 30, memoryAvailable: 100 << 20}, nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- monitor.wait(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("resource wait did not expire: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("low memory caused indefinite resource waiting")
	}
	if !monitor.readyUntil.IsZero() {
		t.Fatal("expired resource wait cached a successful admission")
	}
}

func TestLanguageResourceWaitResumesAfterLoadDrops(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	sampled := make(chan struct{}, 1)
	monitor := &languageResourceMonitor{
		interval: time.Millisecond,
		sample: func(context.Context) (languageSystemSample, error) {
			available := uint64(500)
			if busy.Load() {
				available = 50
			}
			select {
			case sampled <- struct{}{}:
			default:
			}
			return languageSystemSample{cpuPercent: 10, memoryTotal: 1000, memoryAvailable: available}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- monitor.wait(ctx) }()
	select {
	case <-sampled:
	case <-time.After(time.Second):
		t.Fatal("resource sampling did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("admitted work while available memory is low: %v", err)
	default:
	}
	busy.Store(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("did not resume when resources recovered")
	}
}

func TestLanguageResourceWaitCancellationAndSamplingFailure(t *testing.T) {
	t.Run("cancelled cached admission", func(t *testing.T) {
		monitor := &languageResourceMonitor{readyUntil: time.Now().Add(time.Hour)}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := monitor.wait(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("cached admission ignored cancellation: %v", err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		sampled := make(chan struct{}, 1)
		monitor := &languageResourceMonitor{
			interval: time.Hour,
			sample: func(context.Context) (languageSystemSample, error) {
				select {
				case sampled <- struct{}{}:
				default:
				}
				return languageSystemSample{cpuPercent: 95, memoryTotal: 1000, memoryAvailable: 500}, nil
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- monitor.wait(ctx) }()
		<-sampled
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("resource wait ignored cancellation")
		}
	})
	t.Run("sample failed", func(t *testing.T) {
		failure := errors.New("unavailable")
		monitor := &languageResourceMonitor{sample: func(context.Context) (languageSystemSample, error) { return languageSystemSample{}, failure }}
		if err := monitor.wait(context.Background()); !errors.Is(err, failure) {
			t.Fatalf("resource sampling failure was ignored: %v", err)
		}
	})
}

func TestLanguageResourceWaitResamplesAfterAdmissionExpires(t *testing.T) {
	reads := 0
	rechecked := make(chan struct{})
	monitor := &languageResourceMonitor{
		interval: time.Hour,
		sample: func(context.Context) (languageSystemSample, error) {
			reads++
			usage := float64(10)
			if reads > 1 {
				usage = 95
				close(rechecked)
			}
			return languageSystemSample{cpuPercent: usage, memoryTotal: 1000, memoryAvailable: 500}, nil
		},
	}
	if err := monitor.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := monitor.wait(context.Background()); err != nil || reads != 1 {
		t.Fatalf("did not reuse recent admission: reads=%d error=%v", reads, err)
	}
	monitor.readyUntil = time.Now().Add(-time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- monitor.wait(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("expired admission admitted currently busy CPU: %v", err)
	case <-rechecked:
	case <-time.After(time.Second):
		t.Fatal("did not collect a fresh CPU interval")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("busy CPU wait ignored cancellation")
	}
}

func TestLanguageResourceWaitCancelsSampling(t *testing.T) {
	started := make(chan struct{})
	monitor := &languageResourceMonitor{
		sample: func(ctx context.Context) (languageSystemSample, error) {
			close(started)
			<-ctx.Done()
			return languageSystemSample{}, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- monitor.wait(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("sampling did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("sampling ignored cancellation")
	}
}

func TestLanguageSystemResources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sample, err := readLanguageSystemResources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if math.IsNaN(sample.cpuPercent) || sample.cpuPercent < 0 || sample.cpuPercent > 100 ||
		sample.memoryTotal == 0 || sample.memoryAvailable > sample.memoryTotal {
		t.Fatalf("invalid system resources: %+v", sample)
	}
	t.Logf("CPU=%.1f%% available memory=%d/%d", sample.cpuPercent, sample.memoryAvailable, sample.memoryTotal)
}

func TestLanguageSystemResourcesCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := readLanguageSystemResources(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CPU interval ignored cancellation: %v", err)
	}
}
