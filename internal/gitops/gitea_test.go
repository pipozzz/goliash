// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestGitea(t *testing.T) {
	files := map[string]string{
		"apps/checkout/prod/kustomization.yaml": "images:\n  - name: ghcr.io/acme/checkout\n    newTag: 1.4.2\n",
		"apps/web/prod/kustomization.yaml":      "images:\n  - name: ghcr.io/acme/checkout\n    newTag: 1.4.2\n",
	}
	var mu sync.Mutex
	var commit, pull map[string]any
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gt" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		const repo = "/api/v1/repos/acme/deploy"
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && p == repo:
			_, _ = w.Write([]byte(`{"default_branch":"main"}`))
		case r.Method == http.MethodGet && p == repo+"/branches/main":
			_, _ = w.Write([]byte(`{"commit":{"id":"c0ffee"}}`))
		case r.Method == http.MethodGet && p == repo+"/git/trees/c0ffee":
			var tree []map[string]any
			for name, c := range files {
				tree = append(tree, map[string]any{"path": name, "type": "blob", "size": len(c)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tree": tree})
		case r.Method == http.MethodGet && strings.HasPrefix(p, repo+"/contents/"):
			name := strings.TrimPrefix(p, repo+"/contents/")
			c, ok := files[name]
			if !ok || r.URL.Query().Get("ref") != "main" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "sha-" + name, "content": base64.StdEncoding.EncodeToString([]byte(c))})
		case r.Method == http.MethodPost && p == repo+"/contents":
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&commit)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && p == repo+"/pulls":
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&pull)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"html_url":"` + srv.URL + `/acme/deploy/pulls/3"}`))
		case r.Method == http.MethodGet && p == repo+"/pulls/3":
			_, _ = w.Write([]byte(`{"state":"closed","merged":true}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	g := NewGitea("gt", srv.URL+"/")
	ctx := context.Background()
	repo := srv.URL + "/acme/deploy.git"
	if !g.Supports(repo) || g.Supports("https://github.com/acme/deploy") || g.Supports(srv.URL+"/acme") {
		t.Fatal("Supports")
	}
	b := Bump{
		Repo: repo, Path: "apps/checkout/prod", Image: "ghcr.io/acme/checkout", From: "1.4.2", To: "1.5.0",
		Head: "goliash/checkout-1.5.0-prod", Title: "checkout 1.5.0 in prod", Body: "why", CommitMsg: "checkout: 1.4.2 → 1.5.0",
	}
	if tags, err := g.Pinned(ctx, b); err != nil || strings.Join(tags, ",") != "1.4.2" {
		t.Fatalf("Pinned = %v, %v", tags, err)
	}
	pr, err := g.OpenBump(ctx, b)
	if err != nil || pr != srv.URL+"/acme/deploy/pulls/3" {
		t.Fatalf("OpenBump = %q, %v", pr, err)
	}
	ops, _ := commit["files"].([]any)
	if commit["branch"] != "main" || commit["new_branch"] != "goliash/checkout-1.5.0-prod" || len(ops) != 1 {
		t.Fatalf("commit %v", commit)
	}
	op, _ := ops[0].(map[string]any)
	got, _ := base64.StdEncoding.DecodeString(op["content"].(string))
	if op["path"] != "apps/checkout/prod/kustomization.yaml" || op["sha"] != "sha-apps/checkout/prod/kustomization.yaml" || !strings.Contains(string(got), "newTag: 1.5.0") {
		t.Fatalf("file %v %q", op, got)
	}
	if pull["head"] != "goliash/checkout-1.5.0-prod" || pull["base"] != "main" || pull["title"] != "checkout 1.5.0 in prod" {
		t.Fatalf("pull %v", pull)
	}
	if st, err := g.PullState(ctx, pr); err != nil || st != "merged" {
		t.Fatalf("PullState = %q, %v", st, err)
	}
	if repoOf(pr) != srv.URL+"/acme/deploy" {
		t.Fatalf("repoOf = %q", repoOf(pr))
	}
}
