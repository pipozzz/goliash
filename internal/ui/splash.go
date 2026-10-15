// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strconv"
	"sync"
)

// Launch screens for the app installed on an iPhone or iPad: iOS shows a white screen while the app starts
// unless the page names an image of the exact screen size. Goliash draws them, the logo's tiles on its navy, for
// the screens below, once each.

// splashScreen is a device's screen in CSS pixels and its pixel ratio, portrait.
type splashScreen struct{ W, H, Ratio int }

// splashScreens are the iPhones and iPads that can install web apps today.
var splashScreens = []splashScreen{
	{440, 956, 3},
	{430, 932, 3},
	{402, 874, 3},
	{393, 852, 3},
	{428, 926, 3},
	{390, 844, 3},
	{375, 812, 3},
	{414, 896, 3},
	{414, 896, 2},
	{414, 736, 3},
	{375, 667, 2},
	{320, 568, 2},
	{1032, 1376, 2},
	{1024, 1366, 2},
	{834, 1210, 2},
	{834, 1194, 2},
	{820, 1180, 2},
	{834, 1112, 2},
	{810, 1080, 2},
	{768, 1024, 2},
	{744, 1133, 2},
}

// splashMedia is the media query that picks a launch screen.
func splashMedia(s splashScreen) string {
	return fmt.Sprintf("(device-width: %dpx) and (device-height: %dpx) and (-webkit-device-pixel-ratio: %d) and (orientation: portrait)",
		s.W, s.H, s.Ratio)
}

func splashHref(s splashScreen) string {
	return fmt.Sprintf("/splash/%dx%d.png", s.W*s.Ratio, s.H*s.Ratio)
}

var (
	splashMu    sync.Mutex
	splashCache = map[string][]byte{}
)

// splash serves a launch screen of one of the known sizes.
func (s *Server) splash(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("size")
	var pw, ph int
	if _, err := fmt.Sscanf(name, "%dx%d.png", &pw, &ph); err != nil || !knownSplash(pw, ph) {
		http.NotFound(w, r)
		return
	}
	splashMu.Lock()
	b, ok := splashCache[name]
	if !ok {
		var buf bytes.Buffer
		if err := png.Encode(&buf, drawSplash(pw, ph)); err != nil {
			splashMu.Unlock()
			http.Error(w, "could not draw", http.StatusInternalServerError)
			return
		}
		b = buf.Bytes()
		splashCache[name] = b
	}
	splashMu.Unlock()
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = w.Write(b)
}

func knownSplash(pw, ph int) bool {
	for _, sc := range splashScreens {
		if sc.W*sc.Ratio == pw && sc.H*sc.Ratio == ph {
			return true
		}
	}
	return false
}

var (
	splashNavy  = color.RGBA{0x1b, 0x2a, 0x6b, 0xff}
	splashBlue  = color.RGBA{0x8a, 0xa4, 0xff, 0xff}
	splashAmber = color.RGBA{0xff, 0xb3, 0x47, 0xff}
)

// drawSplash draws the logo's nine tiles, as in the icon, in the middle of a navy screen.
func drawSplash(pw, ph int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, pw, ph))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = splashNavy.R, splashNavy.G, splashNavy.B, 0xff
	}
	unit := float64(min(pw, ph)) * 0.30 / 62 // the logo's grid is 62 units wide
	ox, oy := (float64(pw)-62*unit)/2, (float64(ph)-62*unit)/2
	opacity := [3][3]float64{{1, .55, .3}, {.55, 1, 1}, {.3, .55, 1}}
	for row := range 3 {
		for col := range 3 {
			c, a := splashBlue, opacity[row][col]
			if row == 1 && col == 2 {
				c, a = splashAmber, 1
			}
			roundedTile(img, ox+float64(col)*22*unit, oy+float64(row)*22*unit, 18*unit, 4.5*unit, c, a)
		}
	}
	return img
}

// roundedTile blends a rounded square onto img, smoothing its edges by sampling each edge pixel 4×4.
func roundedTile(img *image.RGBA, x, y, size, radius float64, c color.RGBA, alpha float64) {
	inside := func(px, py float64) bool {
		dx := max(x+radius-px, px-(x+size-radius), 0)
		dy := max(y+radius-py, py-(y+size-radius), 0)
		return px >= x && px <= x+size && py >= y && py <= y+size && dx*dx+dy*dy <= radius*radius
	}
	for py := int(y); py <= int(y+size)+1; py++ {
		for px := int(x); px <= int(x+size)+1; px++ {
			cover := 0.0
			for sy := range 4 {
				for sx := range 4 {
					if inside(float64(px)+(float64(sx)+.5)/4, float64(py)+(float64(sy)+.5)/4) {
						cover++
					}
				}
			}
			if cover == 0 || !(image.Point{px, py}.In(img.Rect)) {
				continue
			}
			a := alpha * cover / 16
			i := img.PixOffset(px, py)
			img.Pix[i] = uint8(float64(img.Pix[i])*(1-a) + float64(c.R)*a)
			img.Pix[i+1] = uint8(float64(img.Pix[i+1])*(1-a) + float64(c.G)*a)
			img.Pix[i+2] = uint8(float64(img.Pix[i+2])*(1-a) + float64(c.B)*a)
		}
	}
}
