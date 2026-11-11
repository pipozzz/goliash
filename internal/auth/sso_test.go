// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// Single sign-on set up in Goliash: the provider must answer before it is used, and a test sign-in says what a person
// would get without signing them in.
func TestSSOSettingsAndTest(t *testing.T) {
	h := newHarness(t, false)
	p := newFakeProvider(t)
	ctx := context.Background()
	if err := h.auth.ApplySSO(ctx, store.SSOSettings{Issuer: "https://nothing.invalid", ClientID: "goliash"}); err == nil || h.auth.OIDCEnabled() {
		t.Fatal("a provider that does not answer was taken")
	}
	if err := h.auth.ApplySSO(ctx, store.SSOSettings{Issuer: p.srv.URL, ClientID: "goliash", ClientSecret: "s", GroupRoles: "nope"}); err == nil {
		t.Fatal("bad group roles taken")
	}
	err := h.auth.ApplySSO(ctx, store.SSOSettings{
		Issuer: p.srv.URL, ClientID: "goliash", ClientSecret: "s", Name: "Acme",
		GroupRoles: "ops=admin, sre-pay=member@payments, everyone=viewer",
	})
	if err != nil || !h.auth.OIDCEnabled() || h.auth.OIDCName() != "Acme" || len(h.auth.CurrentGroupRoles()) != 3 {
		t.Fatalf("apply: %v", err)
	}

	p.mu.Lock()
	p.email, p.groups = "dev@acme.io", []string{"sre-pay", "everyone"}
	p.mu.Unlock()
	jar, _ := cookiejar.New(nil)
	var landed string
	cl := &http.Client{Jar: jar, CheckRedirect: func(r *http.Request, _ []*http.Request) error {
		if r.URL.Path == "/settings/sso" {
			landed = r.URL.Query().Get("test")
			return http.ErrUseLastResponse
		}
		return nil
	}}
	_, _ = get(t, cl, h.srv.URL+"/auth/oidc/start?test=1")
	res, ok := h.auth.SSOTestResult(landed)
	if !ok || !res.Allowed || res.Email != "dev@acme.io" || res.Result != "Signs in as viewer in every workspace, member in payments." {
		t.Fatalf("test result %+v %v", res, ok)
	}
	if _, err := h.st.GetUserByEmail(ctx, h.ws.OrgID, "dev@acme.io"); !errors.Is(err, store.ErrNotFound) {
		t.Error("a test sign-in created an account")
	}
	if _, who := get(t, cl, h.srv.URL+"/whoami"); who != "" {
		t.Errorf("a test sign-in signed in: %q", who)
	}
	if _, again := h.auth.SSOTestResult(landed); again {
		t.Error("a test result is shown twice")
	}

	// In no group: refused.
	p.mu.Lock()
	p.groups = []string{"marketing"}
	p.mu.Unlock()
	landed = ""
	_, _ = get(t, cl, h.srv.URL+"/auth/oidc/start?test=1")
	if res, _ := h.auth.SSOTestResult(landed); res.Allowed || !strings.Contains(res.Result, "no group") {
		t.Errorf("no group: %+v", res)
	}

	h.auth.SetOIDC(nil)
	if h.auth.OIDCEnabled() {
		t.Error("still on")
	}
}
