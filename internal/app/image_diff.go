// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
)

func hasImageDiff(diff gitrepo.Diff) bool {
	return diff.Old != nil && diff.New != nil &&
		(diff.Old.Present && diff.Old.ImageFormat != "" || diff.New.Present && diff.New.ImageFormat != "")
}

type nativeImageDiff struct {
	pictures      [2]nativeBrowserImage
	width, height float32
	zoom          float32
	panX, panY    float32
	ready         bool
}

// 单张位图解码开始后不可取消。快速切换文件时让新请求等待，避免多个过期解码同时占用内存。
var imageDiffDecodeSlot = make(chan struct{}, 1)

func decodeImageDiff(ctx context.Context, sources [2]*gitrepo.FileContent) (pictures [2]nativeBrowserImage) {
	select {
	case imageDiffDecodeSlot <- struct{}{}:
		defer func() { <-imageDiffDecodeSlot }()
	case <-ctx.Done():
		return pictures
	}
	for index, source := range sources {
		if ctx.Err() != nil {
			break
		}
		pictures[index] = decodeBrowserImage(source)
	}
	return pictures
}

func (app *nativeApp) imageDiffPanel(c *ui.Context) bool {
	if !hasImageDiff(app.diff) {
		return false
	}
	sources := app.fileViewer.sources
	panel := ui.Column(c).Key(sources).FillWidth().MinHeight(0)
	text := ui.Local(panel, "text", func() bool { return false })
	state := ui.Local(panel, "images", func() nativeImageDiff { return nativeImageDiff{zoom: 1} })
	cancel := ui.Local(panel, "decode", func() context.CancelFunc {
		ctx, cancel := context.WithCancel(app.ctx)
		post := app.post
		go func() {
			pictures := decodeImageDiff(ctx, sources)
			if ctx.Err() == nil {
				post(func() {
					if ctx.Err() == nil {
						state.setPictures(pictures)
					}
				})
			}
		}()
		return cancel
	})
	panel.OnUnmount(*cancel)
	if !*text {
		panel.Grow(1)
	}
	panel.Children(func() {
		ui.Row(c).FillWidth().Gap(4).Padding(4, 8).AlignItems(ui.Center).Children(func() {
			label := locales.DiffText()
			if *text {
				label = locales.ViewImage()
			}
			ui.Button(c, label).OnClick(func() { *text = !*text })
			if *text {
				app.diffLayoutControl(c)
				return
			}
			if state.width > 0 {
				state.zoomControls(c)
			}
		})
		if *text {
			return
		}
		if !state.ready {
			ui.Column(c).FillWidth().Grow(1).Center().Children(func() { ui.Spinner(c).Size(16, 16) })
			return
		}
		state.panes(c, sources)
	})
	return !*text
}

func (state *nativeImageDiff) setPictures(pictures [2]nativeBrowserImage) {
	state.pictures, state.ready = pictures, true
	for _, preview := range pictures {
		if preview.picture != nil {
			width, height := ui.ImageSize(preview.picture)
			state.width, state.height = max(state.width, width), max(state.height, height)
		}
	}
}

func (state *nativeImageDiff) zoomControls(c *ui.Context) {
	zoomOut, zoomIn := locales.ImageDiffZoomOut(), locales.ImageDiffZoomIn()
	ui.Button(c, "−").Label(zoomOut).Tooltip(zoomOut).Disabled(state.zoom <= 0.25).
		OnClick(func() { state.scaleBy(0.5) })
	ui.Text(c, fmt.Sprintf("%g×", state.zoom)).SingleLine()
	ui.Button(c, "+").Label(zoomIn).Tooltip(zoomIn).Disabled(state.zoom >= 8).
		OnClick(func() { state.scaleBy(2) })
	ui.Button(c, locales.ImageFitToView()).OnClick(func() {
		state.zoom, state.panX, state.panY = 1, 0, 0
	})
}

func (state *nativeImageDiff) scaleBy(factor float32) {
	state.zoom *= factor
	state.panX *= factor
	state.panY *= factor
}

func (state *nativeImageDiff) panes(c *ui.Context, sources [2]*gitrepo.FileContent) {
	ui.Row(c).FillWidth().Grow(1).MinHeight(0).Gap(1).AlignItems(ui.Stretch).Children(func() {
		for index, source := range sources {
			preview := state.pictures[index]
			label := fileVersionLabel(source)
			imageLabel := label + " · " + source.Path
			if sources[0].Path != sources[1].Path {
				label = imageLabel
			}
			ui.Column(c).Key(index).Basis(0).Grow(1).MinWidth(0).MinHeight(0).Clip().Children(func() {
				ui.Text(c, label).FillWidth().SingleLine().Tooltip(label).Padding(4, 8).TextColor(c.Theme().TextMuted)
				area := ui.Box(c).FillWidth().Grow(1).MinHeight(0).Clip()
				if preview.picture == nil {
					message := locales.ImagePreviewUnavailable()
					switch {
					case source.Unsupported:
						message = locales.FileContentUnsupported()
					case !source.Present:
						message = locales.AbsentVersion()
					case preview.tooLarge:
						message = locales.ImagePreviewTooLarge()
					}
					area.Center().Children(func() { ui.Text(c, message).TextColor(c.Theme().TextMuted) })
					return
				}
				area.Role(ui.RoleImage).Label(imageLabel).Cursor(ui.CursorGrab)
				if dx, dy, held := area.Dragged(); held {
					state.panX, state.panY = state.panX+dx, state.panY+dy
					area.Cursor(ui.CursorGrabbing)
				}
				width, height := ui.ImageSize(preview.picture)
				area.Draw(func(p *ui.Painter, bounds ui.Rect) {
					if box := state.imageBox(bounds, width, height); box.W > 0 && box.H > 0 {
						p.Clip(bounds, 0, func() { drawImageDiff(p, box, preview.picture) })
					}
				})
			})
		}
	})
	if state.pictures[0].incomplete || state.pictures[1].incomplete {
		ui.Text(c, locales.ImagePreviewPartialSVG()).Padding(4, 8).TextColor(c.Theme().TextMuted)
	}
}

func drawImageDiff(p *ui.Painter, box ui.Rect, picture ui.ImageSource) {
	if _, svg := picture.(*ui.SVG); svg {
		// MyGo 超过 4096 物理像素会跳过 SVG。放大已有的有限尺寸绘制，避免缩放时图片消失。
		if factor := max(box.W, box.H) * p.Scale() / 2048; factor > 1 {
			box.W, box.H = box.W/factor, box.H/factor
			p.Transform(ui.Scaling(factor, factor), box.X, box.Y, func() { drawBrowserImage(p, box, picture) })
			return
		}
	}
	drawBrowserImage(p, box, picture)
}

func (state *nativeImageDiff) imageBox(area ui.Rect, width, height float32) ui.Rect {
	if area.W <= 16 || area.H <= 16 || state.width <= 0 || state.height <= 0 {
		return ui.Rect{}
	}
	scale := min(1, (area.W-16)/state.width, (area.H-16)/state.height) * state.zoom
	return ui.Rect{
		X: area.X + (area.W-state.width*scale)/2 + state.panX,
		Y: area.Y + (area.H-state.height*scale)/2 + state.panY,
		W: width * scale, H: height * scale,
	}
}
