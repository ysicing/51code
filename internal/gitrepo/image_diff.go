// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"path/filepath"
	"strings"
)

func newImageAwareFileContent(path, revision string) *FileContent {
	side := &FileContent{Path: path, Revision: revision}
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".svg", ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp":
		side.ImageFormat = ext[1:]
	}
	return side
}
