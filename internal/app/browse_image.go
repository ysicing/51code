// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"image"
	"math"
	"strings"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

const (
	browserImageMaxSide   = 8192
	browserImageMaxPixels = 16_000_000
)

type nativeBrowserImage struct {
	picture    ui.ImageSource
	tooLarge   bool
	incomplete bool
}

func decodeBrowserImage(content *gitrepo.FileContent) nativeBrowserImage {
	if content == nil || content.ImageFormat == "" || !content.Present || content.Unsupported {
		return nativeBrowserImage{}
	}
	if content.Truncated {
		return nativeBrowserImage{tooLarge: true}
	}
	if content.ImageFormat == "svg" {
		svg, err := ui.ParseSVG([]byte(content.Text))
		if err == nil {
			width, height := svg.Size()
			if width > 0 && height > 0 && max(width, height) <= math.MaxFloat32 {
				return nativeBrowserImage{picture: svg, incomplete: len(svg.Unsupported()) > 0}
			}
		}
		return nativeBrowserImage{}
	}
	info, _, err := image.DecodeConfig(strings.NewReader(content.ImageData))
	if err != nil || info.Width < 1 || info.Height < 1 {
		return nativeBrowserImage{}
	}
	if max(info.Width, info.Height) > browserImageMaxSide || info.Width*info.Height > browserImageMaxPixels {
		return nativeBrowserImage{tooLarge: true}
	}
	bitmap, err := ui.DecodeBitmap([]byte(content.ImageData))
	if err != nil {
		return nativeBrowserImage{}
	}
	return nativeBrowserImage{picture: bitmap}
}

func (preview nativeBrowserImage) view(c *ui.Context, path string, actualSize bool) {
	if preview.picture == nil {
		message := locales.ImagePreviewUnavailable()
		if preview.tooLarge {
			message = locales.ImagePreviewTooLarge()
		}
		ui.Column(c).Grow(1).FillWidth().MinHeight(0).Center().Padding(20).Children(func() {
			ui.Text(c, message).TextColor(c.Theme().TextMuted)
		})
		return
	}
	width, height := ui.ImageSize(preview.picture)
	if actualSize {
		ui.ScrollBoth(c).Grow(1).FillWidth().MinHeight(0).Padding(16).Children(func() {
			ui.Box(c).Size(width, height).Role(ui.RoleImage).Label(path).
				Draw(func(p *ui.Painter, box ui.Rect) { drawBrowserImage(p, box, preview.picture) })
		})
		return
	}
	ui.Box(c).Grow(1).FillWidth().MinHeight(0).Role(ui.RoleImage).Label(path).
		Draw(func(p *ui.Painter, area ui.Rect) {
			scale := min(1, (area.W-32)/width, (area.H-32)/height)
			if _, ok := preview.picture.(*ui.SVG); ok {
				longSide := max(width, height)
				scale = min((area.W-32)/width, (area.H-32)/height, max(1, 160/longSide), 1200/longSide)
			}
			if scale <= 0 {
				return
			}
			w, h := width*scale, height*scale
			box := ui.Rect{X: area.X + (area.W-w)/2, Y: area.Y + (area.H-h)/2, W: w, H: h}
			drawBrowserImage(p, box, preview.picture)
		})
	if preview.incomplete {
		ui.Text(c, locales.ImagePreviewPartialSVG()).TextColor(c.Theme().TextMuted).Padding(6, 12).MaxLines(2)
	}
}

func drawBrowserImage(p *ui.Painter, box ui.Rect, source ui.ImageSource) {
	p.Fill(box, ui.RGB(235, 235, 235), 0)
	cell := max(10, max(box.W, box.H)/96)
	for y, row := float32(0), 0; y < box.H; y, row = y+cell, row+1 {
		for x := float32(row%2) * cell; x < box.W; x += 2 * cell {
			p.Fill(ui.Rect{X: box.X + x, Y: box.Y + y, W: min(cell, box.W-x), H: min(cell, box.H-y)},
				ui.RGB(200, 200, 200), 0)
		}
	}
	p.Image(source, box, ui.Contain)
}
