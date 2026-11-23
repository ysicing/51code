// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strconv"
	"strings"
)

type RemotePhase uint8

const (
	RemotePreparing RemotePhase = iota
	RemoteCompressing
	RemoteSending
	RemoteReceiving
	RemoteResolving
	RemoteConfirming
	RemoteUpdating
	RemoteRefreshing
)

type RemoteProgress struct {
	Phase   RemotePhase
	Percent int
}

const RemotePercentUnknown = -1

type remoteProgressKey struct{}

// WithRemoteProgress 为当前显式同步操作订阅进度。回调在后台串行执行。
func WithRemoteProgress(ctx context.Context, report func(RemoteProgress)) context.Context {
	return context.WithValue(ctx, remoteProgressKey{}, report)
}

func remoteProgressReporter(ctx context.Context) func(RemoteProgress) {
	report, _ := ctx.Value(remoteProgressKey{}).(func(RemoteProgress))
	return report
}

func withRemoteProgressFlag(args []string) []string {
	index := 0
	for index+2 < len(args) && args[index] == "-c" {
		index += 2
	}
	if index < len(args) && (args[index] == "push" || args[index] == "fetch") {
		return slices.Insert(slices.Clone(args), index+1, "--progress")
	}
	return args
}

type remoteProgressWriter struct {
	report  func(RemoteProgress)
	details io.Writer
	pending []byte
	long    bool
}

func (writer *remoteProgressWriter) Write(data []byte) (int, error) {
	size := len(data)
	for len(data) > 0 {
		end := bytes.IndexAny(data, "\r\n")
		if end < 0 {
			writer.append(data)
			break
		}
		writer.append(data[:end])
		writer.flush()
		data = data[end+1:]
	}
	return size, nil
}

func (writer *remoteProgressWriter) append(data []byte) {
	const progressLineLimit = 8 << 10
	if !writer.long && len(writer.pending)+len(data) <= progressLineLimit {
		writer.pending = append(writer.pending, data...)
		return
	}
	if !writer.long {
		_, _ = writer.details.Write(writer.pending)
		writer.pending = writer.pending[:0]
		writer.long = true
	}
	_, _ = writer.details.Write(data)
}

func (writer *remoteProgressWriter) flush() {
	if writer.long {
		_, _ = io.WriteString(writer.details, "\n")
		writer.long = false
	} else if len(writer.pending) != 0 {
		if progress, ok := parseRemoteProgress(string(writer.pending)); ok {
			writer.report(progress)
		} else {
			_, _ = writer.details.Write(writer.pending)
			_, _ = io.WriteString(writer.details, "\n")
		}
	}
	writer.pending = writer.pending[:0]
}

func parseRemoteProgress(line string) (RemoteProgress, bool) {
	line, fromRemote := strings.CutPrefix(strings.TrimSpace(line), "remote: ")
	name, value, found := strings.Cut(strings.TrimSpace(line), ":")
	if !found {
		return RemoteProgress{}, false
	}
	var phase RemotePhase
	switch name {
	case "Enumerating objects", "Counting objects":
		phase = RemotePreparing
	case "Compressing objects":
		phase = RemoteCompressing
	case "Writing objects":
		phase = RemoteSending
	case "Receiving objects":
		phase = RemoteReceiving
	case "Resolving deltas":
		phase = RemoteResolving
	default:
		return RemoteProgress{}, false
	}
	value = strings.TrimSpace(value)
	number, remaining, hasPercent := strings.Cut(value, "%")
	if !hasPercent {
		number, _, _ = strings.Cut(value, ",")
	}
	count, err := strconv.Atoi(strings.TrimSpace(number))
	if err != nil || count < 0 || hasPercent && count > 100 {
		return RemoteProgress{}, false
	}
	if !hasPercent {
		return RemoteProgress{Phase: phase, Percent: RemotePercentUnknown}, true
	}
	if !fromRemote && phase == RemoteSending && count == 100 && strings.HasSuffix(remaining, "done.") {
		return RemoteProgress{Phase: RemoteConfirming, Percent: RemotePercentUnknown}, true
	}
	return RemoteProgress{Phase: phase, Percent: count}, true
}
