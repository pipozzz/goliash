// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// Adding a channel, or a ticket tracker, is in sight, and the link opens the form with its type.
func TestAddChannelInSight(t *testing.T) {
	e := newUIEnv(t)
	admin := e.as(store.RoleAdmin)
	_, page := get(t, admin, e.srv.URL+"/notifications", nil)
	if !strings.Contains(page, `href="/notifications?add=shortcut#add-channel"`) || strings.Contains(page, `id="add-channel" open`) {
		t.Fatal("no tracker links, or the form opens by itself")
	}
	_, page = get(t, admin, e.srv.URL+"/notifications?add=shortcut", nil)
	if !strings.Contains(page, `id="add-channel" open`) || !strings.Contains(page, `value="shortcut" selected`) {
		t.Fatal("the form does not open with Shortcut")
	}
	if _, page = get(t, admin, e.srv.URL+"/notifications?add=push", nil); strings.Contains(page, `id="add-channel" open`) {
		t.Error("web push offered")
	}
}

// An empty list of vulnerabilities says why.
func TestVulnsEmptySaysWhy(t *testing.T) {
	e := newUIEnv(t)
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/security/vulns?env=all", nil)
	if !strings.Contains(page, "No packages known yet") {
		t.Fatal("empty vulnerabilities page without a reason")
	}
}
