// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestBaseStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identifiers/purl":
			_, _ = w.Write([]byte(`{"result":[{"identifier":"pkg:docker/library/node","product":{"name":"nodejs"}}]}`))
		case "/products/nodejs":
			_, _ = w.Write([]byte(`{"result":{"releases":[
				{"name":"18","isEol":true,"eolFrom":"2025-04-30"},
				{"name":"22","isEol":false,"eolFrom":"2027-04-30"}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	e := NewEOL()
	e.BaseURL = srv.URL
	c := &Checker{eol: e}
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()

	old := c.BaseStatus(ctx, "docker.io/library/node:18-alpine@sha256:abc", now)
	if old.Short != "node:18-alpine" || old.Product != "nodejs" || old.Cycle != "18" || old.EOL != "2025-04-30" || !old.Passed || old.Soon {
		t.Errorf("node 18: %+v", old)
	}
	if cur := c.BaseStatus(ctx, "node:22", now); cur.Passed || cur.Soon || cur.Cycle != "22" {
		t.Errorf("node 22: %+v", cur)
	}
	if other := c.BaseStatus(ctx, "gcr.io/distroless/static:nonroot", now); other.Product != "" || other.Short != "gcr.io/distroless/static:nonroot" {
		t.Errorf("unlisted: %+v", other)
	}
	var none *Checker
	if st := none.BaseStatus(ctx, "node:18", now); st.Short != "node:18" || st.Product != "" {
		t.Errorf("without a checker: %+v", st)
	}
}

func TestBaseImageOverride(t *testing.T) {
	svc := store.Service{BaseImage: "docker.io/library/node:18"}
	if got := BaseImage(svc); got != "docker.io/library/node:18" {
		t.Errorf("declared: %q", got)
	}
	svc.VersionPolicy, _ = json.Marshal(Policy{BaseImage: "node:22"})
	if got := BaseImage(svc); got != "node:22" {
		t.Errorf("override: %q", got)
	}
	svc.VersionPolicy, _ = json.Marshal(Policy{BaseImage: "none"})
	if got := BaseImage(svc); got != "" {
		t.Errorf("none: %q", got)
	}
}
