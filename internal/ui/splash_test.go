// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"image/png"
	"net/http"
	"strings"
	"testing"
)

func TestSplash(t *testing.T) {
	e := newUIEnv(t)
	resp, err := http.Get(e.srv.URL + "/splash/1290x2796.png") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	img, err := png.Decode(resp.Body)
	if err != nil || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("splash: %v %s", err, resp.Header.Get("Content-Type"))
	}
	if b := img.Bounds(); b.Dx() != 1290 || b.Dy() != 2796 {
		t.Fatalf("size %v", b)
	}
	// Navy at the corner, the logo's bright middle tile in the centre.
	if r, g, b, _ := img.At(5, 5).RGBA(); r>>8 != 0x1b || g>>8 != 0x2a || b>>8 != 0x6b {
		t.Errorf("corner %x %x %x", r>>8, g>>8, b>>8)
	}
	if r, g, b, _ := img.At(645, 1398).RGBA(); r>>8 != 0x8a || g>>8 != 0xa4 || b>>8 != 0xff {
		t.Errorf("centre %x %x %x", r>>8, g>>8, b>>8)
	}
	if code, _ := get(t, http.DefaultClient, e.srv.URL+"/splash/100x100.png", nil); code != http.StatusNotFound {
		t.Errorf("an unknown size: %d", code)
	}
	_, page := get(t, http.DefaultClient, e.srv.URL+"/login", nil)
	if strings.Count(page, `rel="apple-touch-startup-image"`) != len(splashScreens) || !strings.Contains(page, `href="/splash/1290x2796.png"`) {
		t.Error("the launch screens are not linked")
	}
}
