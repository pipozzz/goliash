// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestReleaseNotesBetweenVersions(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v2.1.0-rc.1","html_url":"u-rc","body":"rc notes"},
			{"tag_name":"v2.0.0","html_url":"u200","published_at":"2026-09-01T10:00:00Z","body":"BREAKING: config moved\n` + strings.Repeat("x", maxNotes) + `"},
			{"tag_name":"v1.9.1","html_url":"u191","body":"Fixes CVE-2026-1234"},
			{"tag_name":"v1.9.0","html_url":"u190","body":"New dashboard"},
			{"tag_name":"v1.8.0","html_url":"u180","body":"old"},
			{"tag_name":"v2.0.1","html_url":"draft","body":"draft","draft":true}
		]`))
	}))
	defer gh.Close()
	client := NewGitHub("")
	client.BaseURL = gh.URL
	c := &Checker{github: client}
	svc := store.Service{Name: "app", VersionPolicy: json.RawMessage(`{"github":"acme/app"}`)}
	ctx := context.Background()

	n, err := c.ReleaseNotes(ctx, svc, "ghcr.io/acme/app", "1.8.0", "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range n.Releases {
		got = append(got, r.Version)
	}
	if strings.Join(got, " ") != "v2.0.0 v1.9.1 v1.9.0" || n.Source != "github:acme/app" {
		t.Fatalf("releases %v from %s", got, n.Source)
	}
	if !strings.HasPrefix(n.Releases[0].Notes, "BREAKING") || len(n.Releases[0].Notes) > maxNotes+10 || n.Releases[0].URL != "u200" {
		t.Errorf("notes of 2.0.0: %d bytes, %s", len(n.Releases[0].Notes), n.Releases[0].URL)
	}

	if n, _ = c.ReleaseNotes(ctx, svc, "ghcr.io/acme/app", "", "1.9.1"); len(n.Releases) != 1 || n.Releases[0].Notes != "Fixes CVE-2026-1234" {
		t.Errorf("only to: %+v", n.Releases)
	}
	if n, _ = c.ReleaseNotes(ctx, svc, "ghcr.io/acme/app", "2.0.0", "2.1.0-rc.1"); len(n.Releases) != 1 || n.Releases[0].Version != "v2.1.0-rc.1" {
		t.Errorf("up to a pre-release: %+v", n.Releases)
	}
	if _, err := c.ReleaseNotes(ctx, svc, "ghcr.io/acme/app", "latest", "2.0.0"); !errors.Is(err, ErrBadVersion) {
		t.Errorf("from latest: %v", err)
	}
	none := store.Service{Name: "plain"}
	if n, err := c.ReleaseNotes(ctx, none, "registry.example.com/plain", "1.0.0", "2.0.0"); err != nil || n.Source != "" || len(n.Releases) != 0 {
		t.Errorf("without a source: %+v %v", n, err)
	}
}
