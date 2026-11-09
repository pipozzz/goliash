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
	"testing"
	"time"
)

func TestCodeOwnersTeamFor(t *testing.T) {
	rules := parseCodeOwners([]byte(`# default owners
*                    @acme/platform
[Payments] @acme/payments
/apps/payments/      @acme/payments @alice   # the team first
apps/checkout/**     @acme/backend/checkout
docs/                @acme/writers
*.md                 @bob
/apps/legacy/        @carol
`))
	for path, want := range map[string]string{
		"apps/payments":                          "payments",
		"apps/payments/prod":                     "payments",
		"/apps/payments/prod/kustomization.yaml": "payments",
		"apps/checkout/prod":                     "checkout",
		"apps/web/prod":                          "platform",
		"nomad/web.nomad.hcl":                    "platform",
		"apps/legacy":                            "", // people own it, no team
		"apps/legacy/README.md":                  "",
		"site/docs/index.yaml":                   "writers", // unanchored: at any depth
		".":                                      "platform",
		"":                                       "platform",
	} {
		if got := teamFor(rules, path); got != want {
			t.Errorf("%q: %q, want %q", path, got, want)
		}
	}
	if got := teamFor(parseCodeOwners([]byte("/apps/ @acme/apps\n")), "other/x"); got != "" {
		t.Errorf("uncovered path: %q", got)
	}
}

// CODEOWNERS is looked for where the forges look, read once an hour per repository and branch.
func TestCodeOwnersFromGitHub(t *testing.T) {
	var reads []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads = append(reads, r.URL.Path+"?"+r.URL.RawQuery)
		if r.URL.Path != "/repos/acme/deploy/contents/CODEOWNERS" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "file", "content": base64.StdEncoding.EncodeToString([]byte("/apps/web/ @acme/web-team\n"))})
	}))
	defer srv.Close()
	gh := NewGitHub("tok", srv.URL)
	gh.host = "github.com"
	co := NewCodeOwners(Router{gh})
	now := time.Now()
	co.now = func() time.Time { return now }
	ctx := context.Background()
	for range 2 {
		team, err := co.Team(ctx, "https://github.com/acme/deploy", "main", "apps/web/prod")
		if err != nil || team != "web-team" {
			t.Fatalf("team %q, %v", team, err)
		}
	}
	if len(reads) != 4 || !strings.HasSuffix(reads[0], "?ref=main") || !strings.HasPrefix(reads[3], "/repos/acme/deploy/contents/CODEOWNERS") {
		t.Fatalf("reads %v", reads)
	}
	// A repository no token takes has no team, and no error.
	if team, err := co.Team(ctx, "https://gitlab.example.com/acme/deploy", "", "apps/web"); err != nil || team != "" {
		t.Fatalf("unsupported: %q %v", team, err)
	}
	// After an hour it is read again.
	now = now.Add(2 * time.Hour)
	_, _ = co.Team(ctx, "https://github.com/acme/deploy", "main", "apps/web")
	if len(reads) != 8 {
		t.Fatalf("not read again: %d", len(reads))
	}
}

// GitLab and Gitea read files too, and a missing one is nil.
func TestReadFileGitLabGitea(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/acme%2Fdeploy/repository/files/.gitlab%2FCODEOWNERS/raw":
			if r.URL.Query().Get("ref") != "HEAD" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte("* @acme/ops\n"))
		case "/api/v1/repos/acme/deploy/contents/CODEOWNERS":
			_ = json.NewEncoder(w).Encode(map[string]string{"type": "file", "content": base64.StdEncoding.EncodeToString([]byte("* @acme/sre\n"))})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	gl := NewCodeOwners(Router{NewGitLab("tok", srv.URL)})
	if team, err := gl.Team(ctx, srv.URL+"/acme/deploy", "", "apps/web"); err != nil || team != "ops" {
		t.Fatalf("gitlab %q %v", team, err)
	}
	gt := NewCodeOwners(Router{NewGitea("tok", srv.URL)})
	if team, err := gt.Team(ctx, srv.URL+"/acme/deploy", "", "apps/web"); err != nil || team != "sre" {
		t.Fatalf("gitea %q %v", team, err)
	}
}
