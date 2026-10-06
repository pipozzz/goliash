// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestBadges(t *testing.T) {
	e := newUIEnv(t)
	svc := e.serviceOf("evil") // the service named <img src=x onerror=alert(1)>
	member := e.as(store.RoleMember)
	_, page := get(t, member, e.srv.URL+"/services/"+svc, nil)
	if !strings.Contains(page, "Badges") || !strings.Contains(page, "Copy markdown") {
		t.Fatal("service page misses its badges")
	}
	m := regexp.MustCompile(`src="(/badge/[^"]+env=prod[^"]*)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no prod badge on the page")
	}
	badge := html.UnescapeString(m[1])

	// Without signing in: the badge loads, with the version, escaped.
	fetch := func(u string) (int, string, http.Header) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+u, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	code, svg, h := fetch(badge)
	if code != 200 || h.Get("Content-Type") != "image/svg+xml" || !strings.Contains(svg, "1.27.2") || !strings.Contains(svg, "· prod") {
		t.Fatalf("badge: %d %s", code, svg)
	}
	if strings.Contains(svg, "<img") || !strings.Contains(svg, "&lt;img") {
		t.Fatal("service name not escaped in the SVG")
	}
	all := regexp.MustCompile(`src="(/badge/[^"?]+\?sig=[^"]*)"`).FindStringSubmatch(page)
	if _, svg, _ := fetch(html.UnescapeString(all[1])); !strings.Contains(svg, "prod 1.27.2") {
		t.Fatalf("every environment badge: %s", svg)
	}
	// A made-up or altered badge is refused.
	if code, _, _ := fetch(strings.Replace(badge, "env=prod", "env=staging", 1)); code != 404 {
		t.Fatalf("badge for another environment with the same signature: %d", code)
	}
	if code, _, _ := fetch("/badge/" + e.ws.ID + "/payments.svg?sig=000000000000000000000000"); code != 404 {
		t.Fatalf("unsigned badge: %d", code)
	}
	// A new key (goliash badges reset) retires every badge handed out.
	if err := e.st.DeleteServerSecret(context.Background(), "badges"); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := fetch(badge); code != 404 {
		t.Fatalf("badge works after a reset: %d", code)
	}
	// Viewers do not get badge links to publish.
	if _, page = get(t, e.as(store.RoleViewer), e.srv.URL+"/services/"+svc, nil); strings.Contains(page, "Copy markdown") {
		t.Error("a viewer is offered badges")
	}
}
