// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// The home view belongs to the person: by device until they pick one, then the same on every device.
func TestHomeView(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	u, _ := e.st.CreateUser(ctx, e.ws.OrgID, "pat@example.com", "", store.RoleMember)
	_ = e.st.SetMembership(ctx, u.ID, e.ws.ID, store.RoleMember)
	device := func() *http.Client {
		link, _ := e.auth.LoginLink(ctx, u)
		jar, _ := cookiejar.New(nil)
		c := &http.Client{Jar: jar}
		get(t, c, link, nil)
		return c
	}
	const iphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148"
	home := func(c *http.Client, ua string) string {
		_, page := get(t, c, e.srv.URL+"/", map[string]string{"User-Agent": ua})
		switch {
		case strings.Contains(page, "tiles-live"):
			return "tiles"
		case strings.Contains(page, `id="matrix-panel"`):
			return "table"
		}
		return "?"
	}
	laptop, phoneC := device(), device()
	if got := home(laptop, "Mozilla/5.0 (Macintosh)"); got != "table" {
		t.Errorf("a computer by default: %s", got)
	}
	if got := home(phoneC, iphone); got != "tiles" {
		t.Errorf("a phone by default: %s", got)
	}

	// Switching to the tiles on the laptop makes them the home view on the phone's other session too.
	get(t, laptop, e.srv.URL+"/tiles", nil)
	if got, _ := e.st.GetUser(ctx, u.ID); got.HomeView != "tiles" {
		t.Fatalf("saved %q", got.HomeView)
	}
	other := device()
	if got := home(other, "Mozilla/5.0 (Macintosh)"); got != "tiles" {
		t.Errorf("another device after choosing tiles: %s", got)
	}
	get(t, other, e.srv.URL+"/?view=table", nil)
	if got := home(phoneC, iphone); got != "table" {
		t.Errorf("the phone after choosing the table: %s", got)
	}

	// Back to automatic on the account page.
	post(t, laptop, e.srv.URL+"/account/home", url.Values{"home_view": {""}})
	if got := home(phoneC, iphone); got != "tiles" {
		t.Errorf("automatic again on the phone: %s", got)
	}
	if code, body, _ := post(t, laptop, e.srv.URL+"/account/home", url.Values{"home_view": {"evil"}}); code >= 500 || !strings.Contains(body, "Pick a home page") {
		t.Errorf("an unknown view: %d", code)
	}
}
