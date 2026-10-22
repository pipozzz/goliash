// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestSecurityIn(t *testing.T) {
	for notes, want := range map[string]string{
		"This is a security release.\n* (CVE-2026-1234) libuv: fix\n* GHSA-abcd-efgh-ijkl in undici, again CVE-2026-1234": "CVE-2026-1234, GHSA-ABCD-EFGH-IJKL",
		"Fixes a critical vulnerability in the parser":                                                                    "security",
		"Security fixes:\n- tighten TLS":                                                                                  "security",
		"Built with Go 1.26.10 (GO-2026-6809)":                                                                            "GO-2026-6809",
		"New dashboard, faster startup. Security headers are now default.":                                                "",
		"": "",
	} {
		if got := SecurityIn(notes); got != want {
			t.Errorf("SecurityIn(%q) = %q, want %q", notes, got, want)
		}
	}
}

// php.net's feed says which PHP releases are security releases; a version with a variant (8.3.36-fpm) counts.
func TestPHPFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("version") != "8" {
			t.Errorf("asked %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"8.3.36":{"tags":["security"]},"8.3.34":{"tags":[]},"8.4.27":{"tags":["security"]}}`))
	}))
	defer srv.Close()
	old := phpNetURL
	phpNetURL = srv.URL + "/?json&version={major}"
	defer func() { phpNetURL = old }()

	feed, err := newPHPFeed().securityReleases(context.Background(), 8)
	if err != nil || !feed["8.3.36"] || feed["8.3.34"] || !feed["8.4.27"] {
		t.Fatalf("feed %v %v", feed, err)
	}
}

// A newly flagged release newer than what runs is announced once; old news is not.
func TestSecurityRelease(t *testing.T) {
	now := time.Date(2026, 10, 21, 15, 0, 0, 0, time.UTC)
	svc := store.Service{ID: "s", Name: "web"}
	flagged := []store.Release{
		{Version: "8.3.35-fpm", Security: "security", DiscoveredAt: now.AddDate(0, 0, -30)},
		{Version: "8.3.36-fpm", Security: "CVE-2026-1, CVE-2026-2", DiscoveredAt: now.Add(-time.Hour)},
		{Version: "8.3.30-fpm", Security: "security", DiscoveredAt: now},
	}
	ev, ok := securityRelease(svc, "8.3.34-fpm", flagged, nil, now)
	if !ok || ev.Type != "security_release" || ev.ToVersion != "8.3.36-fpm" || ev.Note != "CVE-2026-1, CVE-2026-2" || ev.FromVersion != "8.3.34-fpm" {
		t.Fatalf("event %+v %v", ev, ok)
	}
	if _, ok := securityRelease(svc, "8.3.36-fpm", flagged, nil, now); ok {
		t.Error("announced to a service running the fix already")
	}
	if _, ok := securityRelease(svc, "8.3.34-fpm", flagged[:1], nil, now); ok {
		t.Error("a month-old security release is news")
	}
	if ev, ok := securityRelease(svc, "8.3.34-fpm", flagged[:1], []string{"8.3.35-fpm"}, now); !ok || ev.ToVersion != "8.3.35-fpm" {
		t.Error("a release added in this check is news, however Goliash dated it")
	}
}

// An upgrade to a release that fixes vulnerabilities comes right after exploited ones, and says what it fixes.
func TestUpdatesSecurityFix(t *testing.T) {
	o := Overview{ReleaseSecurity: map[string]string{"s|8.3.35": "security", "s|8.3.36": "CVE-2026-1", "s|8.3.20": "CVE-2025-9"}}
	if got := securityUpTo(o, "s", "8.3.34", "8.3.36"); got != "CVE-2026-1" {
		t.Errorf("up to 8.3.36: %q", got)
	}
	if got := securityUpTo(o, "s", "8.3.35", "8.3.35"); got != "" {
		t.Errorf("nothing between: %q", got)
	}
	if got := securityUpTo(o, "s", "8.3.34", "8.3.35"); got != "security" {
		t.Errorf("only a security release: %q", got)
	}
	if u := urgency(Update{SecurityFix: "security", Jump: JumpPatch}); u != UrgencySecurity || !strings.Contains(UrgencyLabels[u], "security") {
		t.Errorf("urgency %d", u)
	}
}

// End to end: a new release the project's feed calls a security release is announced to the service running an
// older one, and its upgrade becomes a security fix.
func TestSecurityReleaseEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"1.28.1":{"tags":["security"]},"1.28.0":{"tags":[]}}`))
	}))
	defer srv.Close()
	old := phpNetURL
	phpNetURL = srv.URL + "/?json&version={major}"
	defer func() { phpNetURL = old }()

	l := newLab(t)
	ctx := context.Background()
	var announced []string
	l.checker.OnEvents(func(_ store.Scope, evs []store.Event) {
		for _, e := range evs {
			if e.Type == "security_release" {
				announced = append(announced, e.FromVersion+"->"+e.ToVersion+"("+e.Note+")")
			}
		}
	})
	l.svc.VersionPolicy = []byte(`{"security":"php.net"}`)
	if err := l.st.UpdateService(ctx, l.svc); err != nil {
		t.Fatal(err)
	}
	l.run(map[string]string{"dev-1": "1.28.0", "stg-1": "1.28.0", "prod-a": "1.27.2", "prod-b": "1.27.2"})
	if err := l.checker.CheckUpstreams(ctx, l.sc); err != nil { // the baseline: nothing announced
		t.Fatal(err)
	}
	l.tags.tags["docker.io/library/nginx"] = append(l.tags.tags["docker.io/library/nginx"], "1.28.1")
	if err := l.checker.CheckService(ctx, l.sc, l.svc.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Join(announced, " ") != "1.27.2->1.28.1(security)" {
		t.Fatalf("security release: %q", announced)
	}
	if err := l.checker.CheckService(ctx, l.sc, l.svc.ID); err != nil {
		t.Fatal(err)
	}
	if len(announced) != 1 {
		t.Fatalf("announced again: %q", announced)
	}
	o, err := LoadOverview(ctx, l.st, l.sc)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range Updates(o, nil, time.Now()) {
		if u.Environment.Name == "prod" && (u.Urgency != UrgencySecurity || u.SecurityFix != "security") {
			t.Fatalf("prod update %+v", u)
		}
	}
}

func TestMoreSecuritySources(t *testing.T) {
	// nginx marks a security change in its notes.
	if got := SecurityIn("Changes with nginx 1.27.4\n\n    *) Security: insufficient check in virtual servers handling.\n    *) Bugfix: …"); got != "security" {
		t.Errorf("nginx Security line: %q", got)
	}
	// A release's own notes page counts; one page for many releases (a #fragment) does not.
	v, _ := ParseVersion("16.5")
	if got := changelogPage("https://www.postgresql.org/docs/release/{version}/", v); got != "https://www.postgresql.org/docs/release/16.5/" {
		t.Errorf("postgres page %q", got)
	}
	if got := changelogPage("https://www.php.net/ChangeLog-8.php#{version}", v); got != "" {
		t.Errorf("a fragment template read: %q", got)
	}
	// Go's release history says which releases include security fixes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/go":
			_, _ = w.Write([]byte(`<p>go1.25.13 (released 2026-08-13) includes security fixes to the go command, and the <code>crypto/tls</code> package, as well as bug fixes.</p>
<p>go1.25.14 (released 2026-08-19) includes fixes to the net/http package.</p>`))
		case "/pg/16.5/":
			_, _ = w.Write([]byte(`<html><body><h2>Security</h2><p>Fix (CVE-2024-10978) &amp; more</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	old := goReleasesURL
	goReleasesURL = srv.URL + "/go"
	defer func() { goReleasesURL = old }()
	f := newTextFeeds()
	got := f.goSecurity(context.Background())
	if !got["1.25.13"] || got["1.25.14"] {
		t.Errorf("go security %v", got)
	}
	if text, ok := f.text(context.Background(), srv.URL+"/pg/16.5/"); !ok || SecurityIn(text) != "CVE-2024-10978" {
		t.Errorf("page text %q %v", text, ok)
	}
	if _, ok := f.text(context.Background(), srv.URL+"/missing"); ok {
		t.Error("a missing page read")
	}
	// A cycle in security-fixes-only support makes each of its releases a security release.
	cycles := []EOLCycle{{Name: "3.12", IsEOAS: true}, {Name: "3.13"}, {Name: "3.1"}}
	for ver, want := range map[string]string{"3.12.7": "3.12", "3.13.1": "3.13", "3.1.4": "3.1", "2.7.18": ""} {
		pv, _ := ParseVersion(ver)
		cy, ok := cycleOf(cycles, pv)
		if (want == "") == ok || cy.Name != want {
			t.Errorf("cycle of %s: %+v %v", ver, cy, ok)
		}
	}
}
