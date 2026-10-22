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
)

func TestBreakingIn(t *testing.T) {
	for _, c := range []struct{ notes, want string }{
		{"## What's changed\n- fix: a typo\n- feat: dark mode", ""},
		{"No breaking changes in this release.\n- fix: a typo", ""},
		{"## ⚠️ Breaking changes\n\n- Removed the `--legacy` flag ([#12](https://x/12))\n- Other", "Removed the --legacy flag (#12)"},
		{"* feat(api)!: rename `items` to `entries`\n* fix: crash", "feat(api)!: rename items to entries"},
		{"BREAKING: configuration moved to /etc/app", "BREAKING: configuration moved to /etc/app"},
		{"- Dropped support for PostgreSQL 12\n- Manual migration required: run `app migrate`", "Dropped support for PostgreSQL 12 · Manual migration required: run app migrate"},
		{"This release is backwards-incompatible with 1.x clients.", "This release is backwards-incompatible with 1.x clients."},
		{"a non-breaking change to the API", ""},
	} {
		if got := BreakingIn(c.notes); got != c.want {
			t.Errorf("BreakingIn(%q) = %q, want %q", c.notes, got, c.want)
		}
	}
	long := "- Breaking change: " + strings.Repeat("word ", 60)
	if got := BreakingIn(long); len([]rune(got)) > breakingWidth || !strings.HasSuffix(got, "…") {
		t.Errorf("a long line is not cut: %q", got)
	}
}

func TestBreakingUpTo(t *testing.T) {
	o := Overview{ReleaseBreaking: map[string]string{
		"s|2.0.0": "Removed the v1 API", "s|1.9.0": "too old", "s|3.0.0": "too new", "s|2.1.0": "Config renamed", "t|2.0.0": "other service",
	}}
	got := breakingUpTo(o, "s", "1.9.0", "2.1.0")
	if strings.Join(got, "|") != "2.0.0: Removed the v1 API|2.1.0: Config renamed" {
		t.Errorf("breakingUpTo = %q", got)
	}
}

// The checker reads what breaks from a release's notes on GitHub, and Updates quotes it on the way to the target.
func TestBreakingEndToEnd(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/nginx/nginx/releases" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[
			{"tag_name":"release-1.28.0","html_url":"x","published_at":"2026-04-23T10:00:00Z","body":"## Breaking changes\n\n- Removed the ssl directive; use listen ... ssl\n"},
			{"tag_name":"release-1.27.3","html_url":"y","published_at":"2025-11-26T10:00:00Z","body":"- fix: a crash"}
		]`))
	}))
	defer gh.Close()
	l := newLab(t)
	ctx := context.Background()
	client := NewGitHub("")
	client.BaseURL = gh.URL
	l.checker.SetGitHub(client)
	l.run(map[string]string{"prod-a": "1.27.2"})
	if err := l.checker.CheckUpstreams(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	if err := l.checker.EvaluateDrift(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	if rel, _ := l.st.GetRelease(ctx, l.sc, l.svc.ID, "1.27.3"); rel.Breaking != "" {
		t.Errorf("1.27.3 breaks: %q", rel.Breaking)
	}
	o, err := LoadOverview(ctx, l.st, l.sc)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range Updates(o, nil, time.Now()) {
		if u.Target == "1.28.0" {
			found = true
			if strings.Join(u.Breaking, "|") != "1.28.0: Removed the ssl directive; use listen ... ssl" {
				t.Errorf("breaking %q", u.Breaking)
			}
		}
	}
	if !found {
		t.Fatal("no update to 1.28.0")
	}
}
