// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"

	_ "embed"
)

//go:embed release.config.json
var releaseConfig []byte

const maxUpdateManifest = 1 << 20

func configureUpdateSource() {
	var config struct {
		DownloadBase string `json:"downloadBase"`
	}
	if err := json.Unmarshal(releaseConfig, &config); err != nil || config.DownloadBase == "" {
		return
	}
	transport := netproxy.Transport().Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	mygo.Updater.SetSource(updateSource{
		feed:    strings.TrimRight(config.DownloadBase, "/") + "/update-" + runtime.GOOS + "-" + runtime.GOARCH + ".json",
		current: mygo.App.Version,
		client:  &http.Client{Transport: transport, CheckRedirect: checkUpdateRedirect},
	})
}

func checkUpdateRedirect(request *http.Request, via []*http.Request) error {
	if request.URL.Scheme != "https" || len(via) >= 10 {
		return http.ErrUseLastResponse
	}
	return nil
}

type updateSource struct {
	feed    string
	current func() string
	client  *http.Client
}

func (source updateSource) Manifest(ctx context.Context, _ bool) (io.ReadCloser, error) {
	body, err := source.Open(ctx, source.feed)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxUpdateManifest+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxUpdateManifest {
		return nil, fmt.Errorf("update manifest exceeds %d bytes", maxUpdateManifest)
	}
	return io.NopCloser(bytes.NewReader(withReleaseHistory(data, source.current()))), nil
}

func (source updateSource) Open(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	delays := [...]time.Duration{time.Second, 3 * time.Second}
	for attempt := 0; ; attempt++ {
		requestCtx, cancel := context.WithCancel(ctx)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		if request.URL.Scheme != "https" {
			cancel()
			return nil, fmt.Errorf("update URL must use HTTPS: %s", rawURL)
		}
		request.Header.Set("User-Agent", "mygo-updater")
		response, err := source.client.Do(request)
		var retryAfter time.Duration
		if err == nil {
			if response.StatusCode == http.StatusOK {
				return &updateBody{ReadCloser: response.Body, cancel: cancel}, nil
			}
			response.Body.Close()
			err = fmt.Errorf("GET %s: %s", rawURL, response.Status)
			switch response.StatusCode {
			case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
				http.StatusServiceUnavailable, http.StatusGatewayTimeout:
				if seconds, parseErr := strconv.Atoi(strings.TrimSpace(response.Header.Get("Retry-After"))); parseErr == nil && seconds > 0 {
					retryAfter = time.Duration(min(seconds, 30)) * time.Second
				}
			default:
				cancel()
				return nil, err
			}
		}
		cancel()
		if attempt == len(delays) {
			return nil, err
		}
		timer := time.NewTimer(max(delays[attempt], retryAfter))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

type updateBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *updateBody) Read(buffer []byte) (int, error) {
	timer := time.AfterFunc(time.Minute, body.cancel)
	defer timer.Stop()
	return body.ReadCloser.Read(buffer)
}

func (body *updateBody) Close() error {
	body.cancel()
	return body.ReadCloser.Close()
}

func withReleaseHistory(data []byte, current string) []byte {
	installed, err := semver.StrictNewVersion(current)
	if err != nil {
		return data
	}
	var manifest map[string]jsontext.Value
	if err := json.Unmarshal(data, &manifest); err != nil {
		return data
	}
	var target, notes string
	var history map[string]string
	if json.Unmarshal(manifest["version"], &target) != nil || json.Unmarshal(manifest["notes"], &notes) != nil {
		return data
	}
	if raw := manifest["releaseHistory"]; raw != nil && json.Unmarshal(raw, &history) != nil {
		history = nil
	}
	latest, err := semver.StrictNewVersion(target)
	if err != nil || !latest.GreaterThan(installed) {
		return data
	}
	var versions []*semver.Version
	for version, entry := range history {
		parsed, err := semver.StrictNewVersion(version)
		if err == nil && parsed.GreaterThan(installed) && parsed.LessThan(latest) && strings.TrimSpace(entry) != "" {
			versions = append(versions, parsed)
		}
	}
	slices.SortFunc(versions, func(left, right *semver.Version) int { return right.Compare(left) })
	groups := releaseNoteGroups{items: make(map[string][]string)}
	if !groups.add(target, notes) {
		return data
	}
	for _, version := range versions {
		if !groups.add(version.Original(), history[version.Original()]) {
			return data
		}
	}
	grouped := groups.markdown()
	if grouped == "" {
		return data
	}
	manifest["notes"], _ = json.Marshal(grouped)
	delete(manifest, "releaseHistory")
	updated, err := json.Marshal(manifest)
	if err != nil || len(updated) > maxUpdateManifest {
		return data
	}
	return updated
}
