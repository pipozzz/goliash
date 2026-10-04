// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGitLabReleases(t *testing.T) {
	var gotToken, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken, gotPath = r.Header.Get("PRIVATE-TOKEN"), r.URL.EscapedPath()
		_, _ = w.Write([]byte(`[
			{"tag_name":"v2.1.0","released_at":"2026-09-01T10:00:00Z","_links":{"self":"https://git.example.com/ops/app/-/releases/v2.1.0"}},
			{"tag_name":"v2.2.0","released_at":"2026-10-30T10:00:00Z","upcoming_release":true}
		]`))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	g := NewGitLab(srv.URL, "s3cret")
	rel, err := g.Releases(context.Background(), host+"/ops/app")
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "s3cret" || gotPath != "/api/v4/projects/ops%2Fapp/releases" {
		t.Fatalf("request: token %q path %q", gotToken, gotPath)
	}
	if len(rel) != 2 || rel[0].Tag != "v2.1.0" || !rel[0].PublishedAt.Equal(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)) || !rel[1].Draft {
		t.Fatalf("releases %+v", rel)
	}
	if !strings.HasSuffix(rel[1].URL, "/ops/app/-/releases/v2.2.0") {
		t.Fatalf("link without _links: %s", rel[1].URL)
	}
	if _, err := g.Releases(context.Background(), "evil.example.org/group/project"); err == nil {
		t.Fatal("a host that is not allowed was called")
	}
	if hosts, _ := gitlabHosts.Load().([]string); len(hosts) != 2 {
		t.Fatalf("allowed hosts %v", hosts)
	}
}

func TestGitLabAnnotationsFromPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"release-1.28.0","released_at":"2026-04-23T10:00:00Z","_links":{"self":"https://gl/r/1.28.0"}}]`))
	}))
	defer srv.Close()
	l := newLab(t)
	ctx := context.Background()
	l.checker.SetGitLab(NewGitLab(srv.URL, ""))
	l.svc.VersionPolicy = json.RawMessage(`{"gitlab":"` + strings.TrimPrefix(srv.URL, "http://") + `/ops/nginx","github_tag_prefix":"release-"}`)
	if err := l.st.UpdateService(ctx, l.svc); err != nil {
		t.Fatal(err)
	}
	l.run(map[string]string{"prod-a": "1.27.2"})
	if err := l.checker.CheckUpstreams(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	rel, _ := l.st.GetRelease(ctx, l.sc, l.svc.ID, "1.28.0")
	if rel.ChangelogURL != "https://gl/r/1.28.0" || rel.PublishedAt.IsZero() {
		t.Fatalf("1.28.0 not annotated from GitLab: %+v", rel)
	}
}
