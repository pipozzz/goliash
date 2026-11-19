// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Each forge says which pull request merged a commit; an open one holding it, or none, is no answer.
func TestCommitPull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/repos/acme/deploy/commits/abc1234/pulls": // GitHub
			_, _ = w.Write([]byte(`[{"html_url":"https://x/acme/deploy/pull/8","title":"WIP","user":{"login":"bob"},"merged_at":null},
				{"html_url":"https://x/acme/deploy/pull/7","title":"Update api to 1.2.0","user":{"login":"renovate[bot]"},"merged_at":"2026-11-02T09:30:00Z"}]`))
		case "/repos/acme/deploy/commits/def5678/pulls":
			_, _ = w.Write([]byte(`[]`))
		case "/api/v4/projects/acme%2Fdeploy/repository/commits/abc1234/merge_requests": // GitLab
			_, _ = w.Write([]byte(`[{"web_url":"https://x/acme/deploy/-/merge_requests/3","title":"Update api","state":"merged","author":{"username":"alice"},"merged_at":"2026-11-02T09:30:00Z"}]`))
		case "/api/v1/repos/acme/deploy/commits/abc1234/pull": // Gitea
			_, _ = w.Write([]byte(`{"html_url":"https://x/acme/deploy/pulls/5","title":"Update api","user":{"login":"carol"},"merged":true,"merged_at":"2026-11-02T09:30:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer srv.Close()
	repo := srv.URL + "/acme/deploy"
	gh := NewGitHub("t", srv.URL) // GitHub Enterprise's API on the test server's host
	for _, c := range []struct {
		name string
		p    CommitPuller
		sha  string
		url  string
		by   string
	}{
		{"github", gh, "abc1234", "https://x/acme/deploy/pull/7", "renovate[bot]"},
		{"github none", gh, "def5678", "", ""},
		{"gitlab", NewGitLab("t", srv.URL), "abc1234", "https://x/acme/deploy/-/merge_requests/3", "alice"},
		{"gitea", NewGitea("t", srv.URL), "abc1234", "https://x/acme/deploy/pulls/5", "carol"},
		{"gitea none", NewGitea("t", srv.URL), "def5678", "", ""},
	} {
		p, ok, err := c.p.CommitPull(t.Context(), repo, c.sha)
		if err != nil || ok != (c.url != "") || p.URL != c.url || p.By != c.by {
			t.Errorf("%s: %+v %v %v", c.name, p, ok, err)
		}
		if ok && p.MergedAt.IsZero() {
			t.Errorf("%s: no merge time", c.name)
		}
	}
	r := Router{NewGitLab("t", srv.URL)}
	if _, _, err := r.CommitPull(t.Context(), "https://elsewhere.example/acme/deploy", "abc1234"); err == nil {
		t.Error("a repository no provider takes was asked")
	}
	if _, ok, _ := Restrict(r, []string{srv.URL + "/other/"}).CommitPull(t.Context(), repo, "abc1234"); ok {
		t.Error("a repository outside the allowed ones was asked")
	}
}
