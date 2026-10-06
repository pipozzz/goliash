// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestInboxMapAll(t *testing.T) {
	e := newUIEnv(t)
	e.addInbox(map[string]string{"pay": "ghcr.io/acme/payments:1.0.0", "cache": "redis:7.4.0"})
	member := e.as(store.RoleMember)
	_, page := get(t, member, e.srv.URL+"/inbox", nil)
	if !strings.Contains(page, "Map all as suggested") {
		t.Fatal("no Map all button")
	}
	form := url.Values{"repo": {"ghcr.io/acme/payments", "docker.io/library/redis"}, "service": {"payments", "redis"}}
	if _, body, _ := post(t, member, e.srv.URL+"/inbox/map-all", form); !strings.Contains(body, "Mapped 2 images") {
		t.Fatalf("map all: %s", body)
	}
	if e.serviceOf("pay") != "payments" || e.serviceOf("cache") != "redis" {
		t.Fatalf("mapped to %q and %q", e.serviceOf("pay"), e.serviceOf("cache"))
	}
}
