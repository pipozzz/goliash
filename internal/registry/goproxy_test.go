// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// Only modules with advisories are asked about; module paths are escaped; answers are remembered.
func TestGoProxyVersion(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case "/index/modules.json":
			_, _ = w.Write([]byte(`[{"path":"github.com/traefik/traefik/v3"},{"path":"github.com/BurntSushi/toml"}]`))
		case "/github.com/traefik/traefik/v3/@v/abc.info":
			_, _ = w.Write([]byte(`{"Version":"v3.1.0"}`))
		case "/github.com/!burnt!sushi/toml/@v/abc.info":
			_, _ = w.Write([]byte(`{"Version":"not a version"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := NewGoProxy(srv.URL + "/")
	p.Index = srv.URL + "/index/modules.json"
	ctx := context.Background()
	for range 2 {
		if v := p.Version(ctx, "github.com/traefik/traefik/v3", "abc"); v != "v3.1.0" {
			t.Fatalf("traefik: %q", v)
		}
	}
	if v := p.Version(ctx, "github.com/BurntSushi/toml", "abc"); v != "" {
		t.Errorf("toml: %q", v)
	}
	if v := p.Version(ctx, "github.com/traefik/traefik/v3", "def"); v != "" {
		t.Errorf("unknown commit: %q", v)
	}
	if v := p.Version(ctx, "git.acme.internal/app", "abc"); v != "" {
		t.Errorf("own module: %q", v)
	}
	want := []string{"/index/modules.json", "/github.com/traefik/traefik/v3/@v/abc.info", "/github.com/!burnt!sushi/toml/@v/abc.info", "/github.com/traefik/traefik/v3/@v/def.info"}
	if !slices.Equal(asked, want) {
		t.Errorf("asked %v", asked)
	}
	if GoProxySetting("off") != nil || GoProxySetting("").URL != DefaultGoProxyURL {
		t.Error("GOLIASH_GOPROXY")
	}
}
