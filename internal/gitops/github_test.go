// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub serves a deploy repository with two files under apps/checkout/prod and records what is written.
func fakeGitHub(t *testing.T, kustomization string) (*httptest.Server, *sync.Map) {
	files := map[string]string{
		"apps/checkout/prod/kustomization.yaml": kustomization,
		"apps/checkout/prod/service.yaml":       "kind: Service\n",
		"apps/web/prod/kustomization.yaml":      "images:\n  - name: ghcr.io/acme/checkout\n    newTag: 1.4.2\n",
	}
	written := &sync.Map{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && p == "/repos/acme/deploy/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"base-sha"}}`))
		case r.Method == http.MethodGet && p == "/repos/acme/deploy/git/trees/base-sha":
			var tree []map[string]any
			for name, c := range files {
				tree = append(tree, map[string]any{"path": name, "type": "blob", "size": len(c)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tree": tree})
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/repos/acme/deploy/contents/"):
			name := strings.TrimPrefix(p, "/repos/acme/deploy/contents/")
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "sha-" + name, "content": base64.StdEncoding.EncodeToString([]byte(files[name]))})
		case r.Method == http.MethodPost && p == "/repos/acme/deploy/git/refs":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			written.Store("ref", b["ref"]+"@"+b["sha"])
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.HasPrefix(p, "/repos/acme/deploy/contents/"):
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			c, _ := base64.StdEncoding.DecodeString(b["content"])
			written.Store(strings.TrimPrefix(p, "/repos/acme/deploy/contents/"), string(c)+"|"+b["sha"]+"|"+b["branch"])
		case r.Method == http.MethodPost && p == "/repos/acme/deploy/pulls":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			written.Store("pull", b["title"].(string)+"|"+b["head"].(string)+"|"+b["base"].(string))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"html_url":"https://github.com/acme/deploy/pull/7","number":7}`))
		case r.Method == http.MethodPost && (p == "/repos/acme/deploy/pulls/7/requested_reviewers" || p == "/repos/acme/deploy/issues/7/labels"):
			raw, _ := io.ReadAll(r.Body)
			written.Store(p, string(raw))
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, written
}

func TestOpenBump(t *testing.T) {
	srv, written := fakeGitHub(t, "images:\n  - name: ghcr.io/acme/checkout\n    newTag: 1.4.2\n")
	g := NewGitHub("tok", srv.URL)
	g.host = "github.com" // the fake API serves github.com's repositories
	url, err := g.OpenBump(context.Background(), Bump{
		Repo: "https://github.com/acme/deploy", Branch: "main", Path: "apps/checkout/prod", Image: "ghcr.io/acme/checkout",
		From: "1.4.2", To: "1.5.0", Head: "goliash/checkout-1.5.0-prod", Title: "checkout 1.5.0 in prod", CommitMsg: "checkout: 1.5.0",
	})
	if err != nil || url != "https://github.com/acme/deploy/pull/7" {
		t.Fatalf("OpenBump = %q, %v", url, err)
	}
	get := func(k string) string { v, _ := written.Load(k); s, _ := v.(string); return s }
	if get("ref") != "refs/heads/goliash/checkout-1.5.0-prod@base-sha" {
		t.Errorf("branch %q", get("ref"))
	}
	if get("apps/checkout/prod/kustomization.yaml") != "images:\n  - name: ghcr.io/acme/checkout\n    newTag: 1.5.0\n|sha-apps/checkout/prod/kustomization.yaml|goliash/checkout-1.5.0-prod" {
		t.Errorf("commit %q", get("apps/checkout/prod/kustomization.yaml"))
	}
	if get("apps/web/prod/kustomization.yaml") != "" || get("apps/checkout/prod/service.yaml") != "" {
		t.Error("a file outside the path, or without the tag, was written")
	}
	if get("pull") != "checkout 1.5.0 in prod|goliash/checkout-1.5.0-prod|main" {
		t.Errorf("pull %q", get("pull"))
	}
}

func TestOpenBumpFindsNothing(t *testing.T) {
	srv, _ := fakeGitHub(t, "images:\n  - name: ghcr.io/acme/checkout\n    digest: sha256:"+strings.Repeat("a", 64)+"\n")
	g := NewGitHub("tok", srv.URL)
	g.host = "github.com"
	_, err := g.OpenBump(context.Background(), Bump{
		Repo: "https://github.com/acme/deploy", Branch: "main",
		Path: "apps/checkout/prod", Image: "ghcr.io/acme/checkout", From: "1.4.2", To: "1.5.0", Head: "h",
	})
	if !errors.Is(err, ErrNoTag) {
		t.Fatalf("err = %v", err)
	}
	if _, err := g.OpenBump(context.Background(), Bump{Repo: "https://gitlab.com/acme/deploy"}); err == nil {
		t.Fatal("a GitLab repository accepted")
	}
}

func TestPinned(t *testing.T) {
	srv, _ := fakeGitHub(t, "images:\n  - name: ghcr.io/acme/checkout\n    newTag: 1.5.0\n")
	g := NewGitHub("tok", srv.URL)
	g.host = "github.com"
	tags, err := g.Pinned(context.Background(), Bump{
		Repo: "https://github.com/acme/deploy", Branch: "main", Path: "apps/checkout/prod", Image: "ghcr.io/acme/checkout",
	})
	if err != nil || strings.Join(tags, ",") != "1.5.0" {
		t.Fatalf("Pinned = %v, %v", tags, err)
	}
}

// A promotion of a whole application changes every image in one pull request, in the files that pin them: one file
// changed twice goes out once, with both.
func TestOpenBumpSeveral(t *testing.T) {
	srv, written := fakeGitHub(t, "images:\n  - name: ghcr.io/acme/checkout\n    newTag: 1.4.2\n  - name: ghcr.io/acme/cart\n    newTag: 3.0.0\n")
	g := NewGitHub("tok", srv.URL)
	g.host = "github.com"
	b := Bump{
		Repo: "https://github.com/acme/deploy", Branch: "main", Path: "apps/checkout/prod", Image: "ghcr.io/acme/checkout",
		From: "1.4.2", To: "1.5.0", Head: "goliash/promote-shop-prod", Title: "Promote shop to prod", CommitMsg: "shop to prod",
		Also: []Pin{{Path: "apps/checkout/prod", Image: "ghcr.io/acme/cart", From: "3.0.0", To: "3.1.0"}},
	}
	if _, err := g.OpenBump(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	got, _ := written.Load("apps/checkout/prod/kustomization.yaml")
	if s, _ := got.(string); !strings.HasPrefix(s, "images:\n  - name: ghcr.io/acme/checkout\n    newTag: 1.5.0\n  - name: ghcr.io/acme/cart\n    newTag: 3.1.0\n|") {
		t.Fatalf("commit %q", s)
	}
	b.Also[0].From = "9.9.9"
	if _, err := g.Plan(context.Background(), b); !errors.Is(err, ErrNoTag) || !strings.Contains(err.Error(), "ghcr.io/acme/cart 9.9.9") {
		t.Fatalf("a pin found nowhere: %v", err)
	}
}

// The policy's reviewers and labels go on the new pull request: people and teams told apart.
func TestOpenBumpReviewers(t *testing.T) {
	srv, written := fakeGitHub(t, "images:\n  - name: ghcr.io/acme/checkout\n    newTag: 1.4.2\n")
	g := NewGitHub("tok", srv.URL)
	g.host = "github.com"
	if _, err := g.OpenBump(context.Background(), Bump{
		Repo: "https://github.com/acme/deploy", Branch: "main", Path: "apps/checkout/prod", Image: "ghcr.io/acme/checkout",
		From: "1.4.2", To: "1.5.0", Head: "goliash/x", Title: "t", CommitMsg: "c",
		Reviewers: []string{"@alice", "@acme/payments"}, Labels: []string{"dependencies"},
	}); err != nil {
		t.Fatal(err)
	}
	get := func(k string) string { v, _ := written.Load(k); s, _ := v.(string); return s }
	if r := get("/repos/acme/deploy/pulls/7/requested_reviewers"); r != `{"reviewers":["alice"],"team_reviewers":["payments"]}` {
		t.Errorf("reviewers %q", r)
	}
	if l := get("/repos/acme/deploy/issues/7/labels"); l != `{"labels":["dependencies"]}` {
		t.Errorf("labels %q", l)
	}
}
