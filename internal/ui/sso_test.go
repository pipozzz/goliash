// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestSSOPage(t *testing.T) {
	e := newUIEnv(t)
	admin := e.as(store.RoleAdmin)
	if code, _ := get(t, admin, e.srv.URL+"/settings/sso", nil); code != 403 {
		t.Errorf("an admin may set up single sign-on: %d", code)
	}
	owner := e.as(store.RoleOwner)
	if _, page := get(t, owner, e.srv.URL+"/settings", nil); !strings.Contains(page, "Set up single sign-on") {
		t.Error("settings do not link single sign-on")
	}
	_, page := get(t, owner, e.srv.URL+"/settings/sso", nil)
	for _, want := range []string{"/auth/oidc/callback", `name="issuer"`, `name="group"`, "every workspace", ">off<"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Mistakes keep what was typed, and change nothing.
	form := url.Values{"issuer": {"idp.example"}, "client_id": {"goliash"}, "client_secret": {"s"}, "group": {"sre"}, "role": {"member"}, "workspace": {""}}
	if _, body, _ := post(t, owner, e.srv.URL+"/settings/sso", form); !strings.Contains(body, "https:// URL") || !strings.Contains(body, `value="sre"`) {
		t.Error("a bad issuer is not explained, or the groups are lost")
	}
	form.Set("issuer", "https://idp.example")
	form["role"] = []string{"admin"}
	form["workspace"] = []string{"payments"}
	if _, body, _ := post(t, owner, e.srv.URL+"/settings/sso", form); !strings.Contains(body, "whole organization") {
		t.Error("admin in one workspace is not refused")
	}
	if e.ui.auth.OIDCEnabled() {
		t.Error("single sign-on on after mistakes")
	}
}
