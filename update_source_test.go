// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReleaseHistoryShowsOnlyVersionsInUpgradeRange(t *testing.T) {
	original := []byte(`{
		"version":"0.5.11","notes":"### 修复\n\n- 修复问题\n\n### 改进\n\n- 第一项\n- 第二项",
		"url":"https://example.test/archive.tar.gz","signature":"archive signature","size":42,
		"releaseHistory":{
			"0.5.8":"更早","0.5.9":"已安装","0.5.10":"### 改进\n\n- 中间版本",
			"0.5.11":"重复目标","0.5.12":"未来版本","invalid":"无效版本"
		}
	}`)
	for _, test := range []struct {
		installed string
		expected  string
	}{
		{"0.5.8", "## 功能改进\n\n- 第一项 `v0.5.11`\n- 第二项 `v0.5.11`\n- 中间版本 `v0.5.10`\n\n## 问题修复\n\n- 修复问题 `v0.5.11`\n\n## 其他更新\n\n- 已安装 `v0.5.9`"},
		{"0.5.9", "## 功能改进\n\n- 第一项 `v0.5.11`\n- 第二项 `v0.5.11`\n- 中间版本 `v0.5.10`\n\n## 问题修复\n\n- 修复问题 `v0.5.11`"},
		{"0.5.10", "## 功能改进\n\n- 第一项 `v0.5.11`\n- 第二项 `v0.5.11`\n\n## 问题修复\n\n- 修复问题 `v0.5.11`"},
	} {
		t.Run(test.installed, func(t *testing.T) {
			var before, after map[string]json.RawMessage
			if err := json.Unmarshal(original, &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(withReleaseHistory(original, test.installed), &after); err != nil {
				t.Fatal(err)
			}
			var notes string
			if err := json.Unmarshal(after["notes"], &notes); err != nil || notes != test.expected {
				t.Fatalf("notes = %q, expected %q, err = %v", notes, test.expected, err)
			}
			for _, field := range []string{"version", "url", "signature", "size"} {
				if !bytes.Equal(before[field], after[field]) {
					t.Errorf("%s changed from %s to %s", field, before[field], after[field])
				}
			}
		})
	}
	for _, installed := range []string{"0.5.11", "0.5.12", ""} {
		if !bytes.Equal(withReleaseHistory(original, installed), original) {
			t.Errorf("manifest changed for version %q", installed)
		}
	}
}

func TestMissingOrInvalidReleaseHistoryShowsTargetNotes(t *testing.T) {
	for _, original := range []string{
		`{"version":"0.5.2","notes":"目标版说明"}`,
		`{"version":"0.5.2","notes":"目标版说明","releaseHistory":{"0.5.1":"   "}}`,
		`{"version":"0.5.2","notes":"目标版说明","releaseHistory":{"0.5.1":3}}`,
	} {
		var result struct{ Notes string }
		if err := json.Unmarshal(withReleaseHistory([]byte(original), "0.5.0"), &result); err != nil {
			t.Fatal(err)
		}
		if want := "## 其他更新\n\n- 目标版说明 `v0.5.2`"; result.Notes != want {
			t.Errorf("fallback = %q, want %q", result.Notes, want)
		}
	}
}

func TestReleaseNotesKeepRemovedAndDeprecatedFeaturesDistinct(t *testing.T) {
	groups := releaseNoteGroups{items: make(map[string][]string)}
	groups.add("0.5.2", strings.Join([]string{
		"### 移除", "- 移除旧按钮，改用 `Shift+F10`。",
		"### 新功能", "* 添加工作树", "  支持自定义路径。",
		"### 废弃", "- 旧入口仍可用。", "### 数据迁移", "- 保留已有配置。",
		"### 修复",
	}, "\r\n"))
	groups.add("0.5.1", "- 未分类的第一项\n- 未分类的第二项")
	want := strings.Join([]string{
		"## 新增功能\n\n- 添加工作树\n  支持自定义路径。 `v0.5.2`",
		"## 已废弃\n\n- 旧入口仍可用。 `v0.5.2`",
		"## 已移除\n\n- 移除旧按钮，改用 `Shift+F10`。 `v0.5.2`",
		"## 其他更新\n\n- 数据迁移：保留已有配置。 `v0.5.2`\n- 未分类的第一项 `v0.5.1`\n- 未分类的第二项 `v0.5.1`",
	}, "\n\n")
	if got := groups.markdown(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestReleaseNoteSizeBudgetAcrossVersions(t *testing.T) {
	groups := releaseNoteGroups{items: make(map[string][]string), bytes: maxUpdateManifest - len("- 甲 `v0.5.2`\n")}
	if !groups.add("0.5.2", "- 甲") || groups.add("0.5.1", "- 乙") {
		t.Fatal("grouped notes did not respect the shared byte limit")
	}
}

func TestUpdateRedirectRequiresHTTPS(t *testing.T) {
	for _, test := range []struct {
		url     string
		hops    int
		refused bool
	}{
		{"https://cdn.example.test/archive", 1, false},
		{"http://cdn.example.test/archive", 1, true},
		{"https://cdn.example.test/archive", 10, true},
	} {
		request := httptest.NewRequest(http.MethodGet, test.url, nil)
		err := checkUpdateRedirect(request, make([]*http.Request, test.hops))
		if (err == http.ErrUseLastResponse) != test.refused {
			t.Errorf("%s after %d hops: %v", test.url, test.hops, err)
		}
	}
}

func TestUpdateSourceServesChangelogAndArchive(t *testing.T) {
	const archive = "signed archive bytes"
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/update-test.json":
			io.WriteString(response, `{"version":"0.5.2","notes":"最新版","releaseHistory":{"0.5.1":"中间版本"}}`)
		case "/archive.tar.gz":
			io.WriteString(response, archive)
		case "/oversize":
			io.WriteString(response, strings.Repeat(" ", maxUpdateManifest+1))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	source := updateSource{feed: server.URL + "/update-test.json", current: func() string { return "0.5.0" }, client: server.Client()}
	manifest, err := source.Manifest(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()
	var update struct{ Notes string }
	if err := json.NewDecoder(manifest).Decode(&update); err != nil {
		t.Fatal(err)
	}
	if update.Notes != "## 其他更新\n\n- 最新版 `v0.5.2`\n- 中间版本 `v0.5.1`" {
		t.Errorf("notes = %q", update.Notes)
	}
	body, err := source.Open(t.Context(), server.URL+"/archive.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if data, err := io.ReadAll(body); err != nil || string(data) != archive {
		t.Fatalf("archive = %q, err = %v", data, err)
	}
	source.feed = server.URL + "/oversize"
	if _, err := source.Manifest(t.Context(), false); err == nil {
		t.Error("oversize manifest was accepted")
	}
}
