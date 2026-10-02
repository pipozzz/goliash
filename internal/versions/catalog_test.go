// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pipozzz/goliash/catalog"
	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// Every catalog entry must be usable: normalized image, valid policy, sane sources.
func TestCatalogEntries(t *testing.T) {
	all, err := catalog.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 20 {
		t.Fatalf("catalog has only %d entries", len(all))
	}
	ghRepo := regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	for image, e := range all {
		if got := ParseImage(image + ":1").Repo(); got != image {
			t.Errorf("%s: not normalized (Goliash sees %s)", image, got)
		}
		if _, err := ParsePolicy(e.PolicyJSON()); err != nil {
			t.Errorf("%s: policy: %v", image, err)
		}
		if e.GitHub == "" && e.Changelog == "" {
			t.Errorf("%s: needs github or changelog", image)
		}
		if e.GitHub != "" && !ghRepo.MatchString(e.GitHub) {
			t.Errorf("%s: github %q is not owner/repo", image, e.GitHub)
		}
		if e.Changelog != "" && !strings.HasPrefix(e.Changelog, "https://") {
			t.Errorf("%s: changelog must be https", image)
		}
	}
}

func TestPolicyFor(t *testing.T) {
	plain := store.Service{VersionPolicy: json.RawMessage(`{}`)}
	p, src, err := PolicyFor(plain, "docker.io/library/postgres")
	if err != nil || src != FromCatalog || p.Track != JumpMinor || !strings.Contains(p.Changelog, "postgresql.org") {
		t.Fatalf("catalog policy %+v %s %v", p, src, err)
	}
	own := store.Service{VersionPolicy: json.RawMessage(`{"track":"major"}`)}
	p, src, _ = PolicyFor(own, "docker.io/library/nginx")
	if src != FromService || p.Track != JumpMajor || p.GitHub != "nginx/nginx" || p.GitHubTagPrefix != "release-" {
		t.Fatalf("own policy keeps catalog sources: %+v %s", p, src)
	}
	p, src, _ = PolicyFor(plain, "ghcr.io/acme/payments-api")
	if src != FromDefault || p.GitHub != "" {
		t.Fatalf("unknown image %+v %s", p, src)
	}
	withGH := store.Service{VersionPolicy: json.RawMessage(`{"github":"acme/payments"}`)}
	if p, _, _ = PolicyFor(withGH, "ghcr.io/acme/payments-api"); p.GitHub != "acme/payments" {
		t.Fatal("service github ignored")
	}
}

func TestReleaseAnnotations(t *testing.T) {
	var calls, notModified atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/repos/nginx/nginx/releases" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("If-None-Match") == `"abc"` {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		_, _ = w.Write([]byte(`[
			{"tag_name":"release-1.28.0","html_url":"https://github.com/nginx/nginx/releases/tag/release-1.28.0","published_at":"2026-04-23T10:00:00Z"},
			{"tag_name":"release-1.27.3","html_url":"https://github.com/nginx/nginx/releases/tag/release-1.27.3","published_at":"2025-11-26T10:00:00Z"},
			{"tag_name":"release-1.29.0","html_url":"x","published_at":"2026-06-24T10:00:00Z","draft":true}
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
	rel, _ := l.st.GetRelease(ctx, l.sc, l.svc.ID, "1.28.0")
	if !rel.PublishedAt.Equal(time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)) || !strings.HasSuffix(rel.ChangelogURL, "release-1.28.0") {
		t.Fatalf("1.28.0 not annotated: %+v", rel)
	}
	if rel, _ := l.st.GetRelease(ctx, l.sc, l.svc.ID, "1.26.2"); !rel.PublishedAt.IsZero() {
		t.Fatalf("release without a GitHub release got a date: %+v", rel)
	}

	// A second check revalidates instead of downloading again.
	client.cache["nginx/nginx"] = ghCache{etag: `"abc"`, releases: client.cache["nginx/nginx"].releases}
	if _, err := client.Releases(ctx, "nginx/nginx"); err != nil || notModified.Load() != 1 {
		t.Fatalf("revalidation: %v, 304s=%d", err, notModified.Load())
	}
}

// labeledTags also answers which source repository each image declares.
type labeledTags struct {
	*fakeTags
	sources map[string]string
	reads   atomic.Int32
}

func (l *labeledTags) ImageSource(_ context.Context, repo, _ string, _ registry.Credentials) (string, error) {
	l.reads.Add(1)
	return l.sources[repo], nil
}

func TestSourceFromImageLabel(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	l.tags.tags["ghcr.io/acme/web"] = []string{"1.27.2", "1.28.0"}
	reg := &labeledTags{fakeTags: l.tags, sources: map[string]string{
		"ghcr.io/acme/web":        "https://github.com/acme/web.git",
		"docker.io/library/nginx": "https://github.com/nginxinc/docker-nginx.git#abc:mainline",
	}}
	l.checker = NewChecker(l.st, reg, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	l.svc.Upstream = "ghcr.io/acme/web"
	if err := l.st.UpdateService(ctx, l.svc); err != nil {
		t.Fatal(err)
	}
	l.run(map[string]string{"prod-a": "1.27.2"})

	check := func() store.Service {
		t.Helper()
		if err := l.checker.CheckUpstreams(ctx, l.sc); err != nil {
			t.Fatal(err)
		}
		svc, _ := l.st.GetService(ctx, l.sc, l.svc.ID)
		return svc
	}
	svc := check()
	if p, _, _ := PolicyFor(svc, "ghcr.io/acme/web"); p.GitHub != "acme/web" {
		t.Fatalf("label not used: %+v (service %+v)", p, svc)
	}
	own := svc
	own.VersionPolicy = json.RawMessage(`{"github":"acme/other"}`)
	if p, _, _ := PolicyFor(own, "ghcr.io/acme/web"); p.GitHub != "acme/other" {
		t.Fatalf("label overrode the service's own github: %s", p.GitHub)
	}

	reads := reg.reads.Load()
	check()
	if reg.reads.Load() != reads {
		t.Fatal("source read again within its TTL")
	}
	l.checker.now = func() time.Time { return time.Now().UTC().Add(8 * 24 * time.Hour) }
	check()
	if reg.reads.Load() != reads+1 {
		t.Fatal("source not read again after its TTL")
	}

	// A new upstream is read at once; Docker Official Images point at their packaging repo.
	l.checker.now = func() time.Time { return time.Now().UTC() }
	l.svc.Upstream = "docker.io/library/nginx"
	_ = l.st.UpdateService(ctx, l.svc)
	svc = check()
	if svc.SourceImage != "docker.io/library/nginx" || LabelGitHub(svc, "docker.io/library/nginx") != "" {
		t.Fatalf("official image: %+v", svc)
	}
	if p, _, _ := PolicyFor(svc, "docker.io/library/nginx"); p.GitHub != "nginx/nginx" {
		t.Fatalf("catalog source lost: %s", p.GitHub)
	}
}
