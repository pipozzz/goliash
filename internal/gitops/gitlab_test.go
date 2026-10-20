// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGitLabOpenBump(t *testing.T) {
	files := map[string]string{
		"envs/prod/values.yaml": "image:\n  repository: registry.gitlab.com/acme/shop/checkout\n  tag: \"1.4.2\"\n",
		"envs/prod/README.yml":  "note: nothing\n",
	}
	var mu sync.Mutex
	var commit, mr map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "glpat" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		p := r.URL.EscapedPath()
		const proj = "/api/v4/projects/acme%2Fdeploy"
		switch {
		case r.Method == http.MethodGet && p == proj:
			_, _ = w.Write([]byte(`{"default_branch":"main"}`))
		case r.Method == http.MethodGet && p == proj+"/repository/tree":
			if r.URL.Query().Get("path") != "envs/prod" || r.URL.Query().Get("ref") != "main" {
				t.Errorf("tree query %s", r.URL.RawQuery)
			}
			var tree []map[string]string
			for name := range files {
				tree = append(tree, map[string]string{"path": name, "type": "blob"})
			}
			_ = json.NewEncoder(w).Encode(tree)
		case r.Method == http.MethodGet && strings.HasPrefix(p, proj+"/repository/files/") && strings.HasSuffix(p, "/raw"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v4/projects/acme/deploy/repository/files/"), "/raw")
			_, _ = w.Write([]byte(files[name]))
		case r.Method == http.MethodPost && p == proj+"/repository/commits":
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&commit)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && p == proj+"/merge_requests":
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&mr)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"web_url":"https://gitlab.example.com/acme/deploy/-/merge_requests/3"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	g := NewGitLab("glpat", srv.URL)
	repo := srv.URL + "/acme/deploy"
	if !g.Supports(repo) || g.Supports("https://github.com/acme/deploy") {
		t.Fatal("Supports")
	}
	url, err := g.OpenBump(context.Background(), Bump{
		Repo: repo, Branch: "HEAD", Path: "envs/prod",
		Image: "registry.gitlab.com/acme/shop/checkout", From: "1.4.2", To: "1.5.0", Head: "goliash/checkout-1.5.0-prod",
		Title: "checkout 1.5.0 in prod", CommitMsg: "checkout: 1.5.0",
	})
	if err != nil || url != "https://gitlab.example.com/acme/deploy/-/merge_requests/3" {
		t.Fatalf("OpenBump = %q, %v", url, err)
	}
	actions, _ := commit["actions"].([]any)
	if commit["branch"] != "goliash/checkout-1.5.0-prod" || commit["start_branch"] != "main" || len(actions) != 1 {
		t.Fatalf("commit %v", commit)
	}
	a := actions[0].(map[string]any)
	if a["file_path"] != "envs/prod/values.yaml" || !strings.Contains(a["content"].(string), `tag: "1.5.0"`) {
		t.Fatalf("action %v", a)
	}
	if mr["source_branch"] != "goliash/checkout-1.5.0-prod" || mr["target_branch"] != "main" || mr["title"] != "checkout 1.5.0 in prod" {
		t.Fatalf("merge request %v", mr)
	}
}

func TestRouter(t *testing.T) {
	r := Router{NewGitHub("a", ""), NewGitLab("b", "https://gitlab.example.com")}
	for repo, want := range map[string]bool{
		"https://github.com/acme/deploy": true, "https://gitlab.example.com/acme/deploy": true,
		"https://gitlab.com/acme/deploy": false, "https://bitbucket.org/acme/deploy": false,
	} {
		if r.Supports(repo) != want {
			t.Errorf("Supports(%s) = %v", repo, !want)
		}
	}
	if NewGitHub("x", "https://github.example.com/api/v3").Supports("https://github.example.com/acme/deploy") != true {
		t.Error("GitHub Enterprise repository not supported")
	}
}

func TestPullState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/repos/acme/deploy/pulls/7":
			_, _ = w.Write([]byte(`{"state":"closed","merged":true,"merged_at":"2026-10-20T09:15:00Z"}`))
		case "/repos/acme/deploy/pulls/8":
			_, _ = w.Write([]byte(`{"state":"closed","merged":false}`))
		case "/api/v4/projects/acme%2Fdeploy/merge_requests/3":
			_, _ = w.Write([]byte(`{"state":"opened"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	gh := NewGitHub("t", srv.URL)
	gh.host = "github.com"
	r := Router{gh, NewGitLab("t", srv.URL)}
	for pull, want := range map[string]string{
		"https://github.com/acme/deploy/pull/7":     "merged",
		"https://github.com/acme/deploy/pull/8":     "closed",
		srv.URL + "/acme/deploy/-/merge_requests/3": "open",
	} {
		if got, err := r.PullState(context.Background(), pull); err != nil || got.State != want {
			t.Errorf("PullState(%s) = %+v, %v; want %q", pull, got, err, want)
		}
	}
	if got, _ := r.PullState(context.Background(), "https://github.com/acme/deploy/pull/7"); !got.MergedAt.Equal(time.Date(2026, 10, 20, 9, 15, 0, 0, time.UTC)) {
		t.Errorf("merged at %v", got.MergedAt)
	}
}
