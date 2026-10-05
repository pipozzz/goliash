// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSetupLink(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	if !h.auth.NoAccounts(ctx) {
		t.Fatal("accounts in an empty database")
	}
	link, err := h.auth.SetupLink(ctx)
	if err != nil || !strings.HasPrefix(link, h.srv.URL+"/setup?token=") {
		t.Fatalf("link %q %v", link, err)
	}
	u, _ := url.Parse(link)
	token := u.Query().Get("token")
	if !h.auth.SetupTokenValid(ctx, token) || h.auth.SetupTokenValid(ctx, "nope") {
		t.Fatal("token validity")
	}

	c := h.client()
	post(t, c, h.srv.URL+"/auth/setup", url.Values{"token": {token}, "email": {"ana@example.com"}, "password": {"short"}, "confirm": {"short"}})
	if !h.auth.NoAccounts(ctx) {
		t.Fatal("weak password created the owner")
	}
	if got := post(t, c, h.srv.URL+"/auth/setup", url.Values{
		"token": {token}, "email": {"Ana@Example.com"}, "name": {"Ana"}, "password": {"correct horse staple"}, "confirm": {"correct horse staple"},
	}); got != "home" {
		t.Fatalf("setup: %q", got)
	}
	if code, body := get(t, c, h.srv.URL+"/whoami"); code != http.StatusOK || !strings.HasPrefix(body, "ana@example.com") {
		t.Fatalf("not signed in: %d %s", code, body)
	}
	owner, err := h.st.GetUserByEmail(ctx, h.ws.OrgID, "ana@example.com")
	if err != nil || owner.Role != "owner" || !owner.HasPassword || owner.Name != "Ana" {
		t.Fatalf("owner %+v %v", owner, err)
	}

	// The link works once, and never once someone has an account.
	if h.auth.SetupTokenValid(ctx, token) || h.auth.NoAccounts(ctx) {
		t.Fatal("setup still open")
	}
	other, _ := h.auth.SetupLink(ctx)
	ou, _ := url.Parse(other)
	if got := post(t, h.client(), h.srv.URL+"/auth/setup", url.Values{
		"token": {ou.Query().Get("token")}, "email": {"mallory@example.com"}, "password": {"correct horse staple"}, "confirm": {"correct horse staple"},
	}); got != "login error=setup" {
		t.Fatalf("second owner: %q", got)
	}
}
