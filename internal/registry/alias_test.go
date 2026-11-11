// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeAlias(t *testing.T) {
	location := "https://registry-1.docker.io/v2/"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			http.NotFound(w, r)
			return
		}
		if location == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, location, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	saved := probeClient
	defer func() { probeClient = saved }()
	probeClient = srv.Client()
	probeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	host := strings.TrimPrefix(srv.URL, "https://")

	if public, ok := ProbeAlias(context.Background(), host); !ok || public != "docker.io" {
		t.Fatalf("alias: %q %v", public, ok)
	}
	if h, repo, _ := splitRepository(host + "/flagsmith/flagsmith"); h != "registry-1.docker.io" || repo != "flagsmith/flagsmith" {
		t.Fatalf("not read from Docker Hub: %s %s", h, repo)
	}
	aliases.Delete(host)

	// A registry of its own, or a redirect anywhere else, is no alias.
	for _, loc := range []string{"", "https://evil.example/v2/", "http://registry-1.docker.io/v2/", "https://registry-1.docker.io/other"} {
		location = loc
		if public, ok := ProbeAlias(context.Background(), host); ok {
			t.Errorf("%q taken as an alias of %s", loc, public)
		}
	}
	if _, ok := ProbeAlias(context.Background(), "evil.example/x"); ok {
		t.Error("a host with a path was probed")
	}
}

func TestProbeRefusesInsideAddresses(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://registry-1.docker.io/v2/", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	if _, ok := ProbeAlias(context.Background(), strings.TrimPrefix(srv.URL, "https://")); ok {
		t.Fatal("the probe reached a loopback address")
	}
}
