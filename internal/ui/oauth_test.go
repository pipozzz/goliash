// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// oauthApp plays an MCP client: PKCE, its redirect, and the calls it makes.
type oauthApp struct {
	t                   *testing.T
	e                   *uiEnv
	base                string // where the app talks to Goliash
	clientID, redirect  string
	verifier, challenge string
}

func newOAuthApp(t *testing.T, e *uiEnv, clientID, redirect string) *oauthApp {
	verifier := strings.Repeat("v3r1f13r-", 6) // 54 characters
	sum := sha256.Sum256([]byte(verifier))
	return &oauthApp{
		t: t, e: e, base: e.srv.URL, clientID: clientID, redirect: redirect, verifier: verifier,
		challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}
}

func (a *oauthApp) authorizeURL(state string) string {
	return a.base + "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {a.clientID}, "redirect_uri": {a.redirect}, "state": {state},
		"code_challenge": {a.challenge}, "code_challenge_method": {"S256"}, "resource": {a.e.srv.URL + "/mcp"},
		"scope": {"goliash:read"},
	}.Encode()
}

// allow signs in the browser's person through the consent page and returns where the app was sent back to.
func (a *oauthApp) allow(browser *http.Client, decision string) *url.URL {
	a.t.Helper()
	code, page := get(a.t, browser, a.authorizeURL("xyz"), nil)
	if code != http.StatusOK || !strings.Contains(page, "to read Goliash?") || !strings.Contains(page, `hx-boost="false"`) {
		a.t.Fatalf("consent page %d: %.400s", code, page)
	}
	form, _ := url.ParseQuery(a.authorizeURL("xyz")[strings.Index(a.authorizeURL("xyz"), "?")+1:])
	form.Set("decision", decision)
	form.Set("workspace", a.e.ws.ID)
	noFollow := *browser
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, a.base+"/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noFollow.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		a.t.Fatalf("decision: %d", resp.StatusCode)
	}
	u, _ := url.Parse(resp.Header.Get("Location"))
	return u
}

func (a *oauthApp) token(form url.Values) (int, map[string]any) {
	a.t.Helper()
	form.Set("client_id", a.clientID)
	resp, err := http.PostForm(a.base+"/oauth/token", form) //nolint:noctx // test
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// whoami is what Goliash makes of a bearer token.
func (e *uiEnv) whoami(token string) (string, string, bool) {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/summary", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	p, ok, err := e.auth.Authenticate(r)
	if err != nil {
		e.t.Fatal(err)
	}
	return p.Role, p.Via, ok
}

func TestOAuthForMCP(t *testing.T) {
	e := newUIEnv(t)
	// Discovery: the resource names its authorization server, which says what it supports.
	_, body := get(t, http.DefaultClient, e.srv.URL+"/.well-known/oauth-protected-resource/mcp", nil)
	if !strings.Contains(body, `"resource":"`+e.srv.URL+`/mcp"`) || !strings.Contains(body, `"authorization_servers":["`+e.srv.URL+`"]`) {
		t.Fatalf("resource metadata: %s", body)
	}
	_, body = get(t, http.DefaultClient, e.srv.URL+"/.well-known/oauth-authorization-server", nil)
	for _, want := range []string{`"issuer":"` + e.srv.URL + `"`, `"code_challenge_methods_supported":["S256"]`, `"client_id_metadata_document_supported":true`, `"authorization_response_iss_parameter_supported":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("server metadata misses %s", want)
		}
	}

	// An older app registers itself (RFC 7591); bad redirects are refused.
	reg := func(js string) (int, map[string]any) {
		resp, err := http.Post(e.srv.URL+"/oauth/register", "application/json", strings.NewReader(js)) //nolint:noctx // test
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, out := reg(`{"client_name":"Evil","redirect_uris":["http://evil.example/cb"]}`); code != http.StatusBadRequest || out["error"] != "invalid_redirect_uri" {
		t.Errorf("plain http redirect accepted: %d %v", code, out)
	}
	code, out := reg(`{"client_name":"Claude Code","redirect_uris":["http://localhost:4000/callback"],"token_endpoint_auth_method":"none"}`)
	clientID, _ := out["client_id"].(string)
	if code != http.StatusCreated || !strings.HasPrefix(clientID, "glsh_cli_") {
		t.Fatalf("register: %d %v", code, out)
	}

	// Not signed in: sign in first, then back to the consent page.
	app := newOAuthApp(t, e, clientID, "http://localhost:53682/callback") // another port: loopback ports vary
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(app.authorizeURL("s")) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" || !strings.Contains(resp.Header.Get("Set-Cookie"), "goliash_next=") {
		t.Errorf("not signed in: %d %s %s", resp.StatusCode, resp.Header.Get("Location"), resp.Header.Get("Set-Cookie"))
	}

	// Only a page someone opens is remembered: not the live stream reconnecting, nor htmx's partial requests.
	for _, h := range []map[string]string{{"Sec-Fetch-Dest": "empty"}, {"HX-Request": "true"}} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+"/updates", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		if resp, err := noFollow.Do(req); err == nil {
			_ = resp.Body.Close()
			if strings.Contains(resp.Header.Get("Set-Cookie"), "goliash_next=") {
				t.Errorf("remembered a request with %v", h)
			}
		}
	}
	sreq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+"/ui/stream", nil)
	if resp, err := noFollow.Do(sreq); err == nil {
		_ = resp.Body.Close()
		if strings.Contains(resp.Header.Get("Set-Cookie"), "goliash_next=") {
			t.Error("remembered the live stream")
		}
	}

	// An admin allows it: the app gets a code, its state and the issuer back. The consent page lets its form
	// redirect to the app's registered address, and only there.
	browser := e.as(store.RoleAdmin)
	creq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, app.authorizeURL("s"), nil)
	if cresp, err := browser.Do(creq); err == nil {
		_ = cresp.Body.Close()
		if csp := cresp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' http://localhost:53682;") {
			t.Errorf("consent CSP: %s", csp)
		}
	}
	back := app.allow(browser, "allow")
	q := back.Query()
	if back.Host != "localhost:53682" || q.Get("state") != "xyz" || q.Get("iss") != e.srv.URL || q.Get("code") == "" {
		t.Fatalf("back to the app: %s", back)
	}

	// The code needs the verifier, and works once.
	if code, out := app.token(url.Values{"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "redirect_uri": {app.redirect}, "code_verifier": {strings.Repeat("x", 50)}}); code != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("wrong verifier: %d %v", code, out)
	}
	back = app.allow(browser, "allow")
	form := url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "redirect_uri": {app.redirect}, "code_verifier": {app.verifier}, "resource": {e.srv.URL + "/mcp"}}
	code, tok := app.token(form)
	access, _ := tok["access_token"].(string)
	refresh, _ := tok["refresh_token"].(string)
	if code != http.StatusOK || !strings.HasPrefix(access, "glsh_oat_") || !strings.HasPrefix(refresh, "glsh_ort_") || tok["token_type"] != "Bearer" {
		t.Fatalf("token: %d %v", code, tok)
	}
	if code, _ := app.token(form); code != http.StatusBadRequest {
		t.Error("a code worked twice")
	}

	// The token reads as the admin, but read only.
	if role, via, ok := e.whoami(access); !ok || role != store.RoleViewer || via != "oauth" {
		t.Errorf("principal: %s %s %v", role, via, ok)
	}

	// Refresh rotates: the old refresh and access tokens stop working.
	code, tok2 := app.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	access2, _ := tok2["access_token"].(string)
	if code != http.StatusOK || access2 == "" || access2 == access {
		t.Fatalf("refresh: %d %v", code, tok2)
	}
	if _, _, ok := e.whoami(access); ok {
		t.Error("the old access token still works")
	}
	if code, _ := app.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}); code != http.StatusBadRequest {
		t.Error("a refresh token worked twice")
	}

	// The account page lists it; revoking ends it.
	_, page := get(t, browser, e.srv.URL+"/account", nil)
	if !strings.Contains(page, "Apps with access") || !strings.Contains(page, "Claude Code") {
		t.Fatal("the app is not on the account page")
	}
	grants, _ := e.st.ListOAuthGrants(context.Background(), grantsUser(t, e, "admin@example.com"))
	if len(grants) != 1 {
		t.Fatalf("grants %d", len(grants))
	}
	post(t, browser, e.srv.URL+"/account/apps/"+grants[0].ID+"/revoke", nil)
	if _, _, ok := e.whoami(access2); ok {
		t.Error("a revoked app still reads")
	}

	// Denying sends the app an error, nothing else.
	back = app.allow(browser, "deny")
	if back.Query().Get("error") != "access_denied" || back.Query().Get("code") != "" {
		t.Errorf("deny: %s", back)
	}

	// A redirect the app did not register is never followed.
	evil := newOAuthApp(t, e, clientID, "https://evil.example/cb")
	code, page = get(t, browser, evil.authorizeURL("s"), nil)
	if code != http.StatusBadRequest || !strings.Contains(page, "did not register") {
		t.Errorf("unregistered redirect: %d", code)
	}
	// Tokens are only for this server.
	u := app.authorizeURL("s")
	u = strings.Replace(u, url.QueryEscape(e.srv.URL+"/mcp"), url.QueryEscape("https://other.example/mcp"), 1)
	resp, err = (&http.Client{Jar: browser.Jar, CheckRedirect: noFollow.CheckRedirect}).Get(u) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if loc, _ := url.Parse(resp.Header.Get("Location")); loc == nil || loc.Query().Get("error") != "invalid_target" {
		t.Errorf("another resource: %s", resp.Header.Get("Location"))
	}
}

func grantsUser(t *testing.T, e *uiEnv, email string) string {
	u, err := e.st.GetUserByEmail(context.Background(), e.ws.OrgID, email)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// An app described by a Client ID Metadata Document: fetched, checked, and its redirect enforced.
func TestOAuthClientMetadataDocument(t *testing.T) {
	e := newUIEnv(t)
	var docURL string
	doc := `{"client_id":"%s","client_name":"Cursor","redirect_uris":["http://127.0.0.1:7000/cb"],"token_endpoint_auth_method":"none"}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := docURL
		if r.URL.Path == "/lying.json" {
			id = "https://elsewhere.example/client.json"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Replace(doc, "%s", id, 1)))
	}))
	defer srv.Close()
	e.ui.oauthHTTP = srv.Client() // the test server is on loopback, which the real client refuses
	docURL = srv.URL + "/client.json"

	browser := e.as(store.RoleMember)
	app := newOAuthApp(t, e, docURL, "http://127.0.0.1:7000/cb")
	code, page := get(t, browser, app.authorizeURL("s"), nil)
	if code != http.StatusOK || !strings.Contains(page, "Allow Cursor to read Goliash?") || !strings.Contains(page, "described by 127.0.0.1") {
		t.Fatalf("consent for a metadata client: %d %.300s", code, page)
	}
	back := app.allow(browser, "allow")
	if _, tok := app.token(url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "redirect_uri": {app.redirect}, "code_verifier": {app.verifier}}); tok["access_token"] == nil {
		t.Fatalf("token for a metadata client: %v", tok)
	}

	lying := newOAuthApp(t, e, srv.URL+"/lying.json", "http://127.0.0.1:7000/cb")
	if code, page := get(t, browser, lying.authorizeURL("s"), nil); code != http.StatusBadRequest || !strings.Contains(page, "names another client_id") {
		t.Errorf("a document naming another client: %d", code)
	}
}

func TestPublicOnlyClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	resp, err := publicOnlyClient().Get(srv.URL) //nolint:noctx // test
	if err == nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Errorf("reached a loopback address: %v", err)
	}
}

// On the public demo, a visitor connects an AI assistant as the demo viewer, without an account: the guard lets
// MCP and its sign-in through, and nobody sees the apps others connected.
func TestOAuthOnPublicDemo(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	viewer, _ := e.st.CreateUser(ctx, e.ws.OrgID, "demo@goliash.dev", "", store.RoleViewer)
	_ = e.st.SetMembership(ctx, viewer.ID, e.ws.ID, store.RoleViewer)
	e.auth.SetDemoUser(viewer)
	guarded := httptest.NewServer(DemoGuard(e.srv.Config.Handler))
	defer guarded.Close()

	resp, err := http.Post(guarded.URL+"/oauth/register", "application/json", //nolint:noctx // test
		strings.NewReader(`{"client_name":"Claude Code","redirect_uris":["http://localhost:4000/callback"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	_ = resp.Body.Close()
	clientID, _ := reg["client_id"].(string)
	if resp.StatusCode != http.StatusCreated || clientID == "" {
		t.Fatalf("register on the demo: %d %v", resp.StatusCode, reg)
	}
	app := newOAuthApp(t, e, clientID, "http://localhost:4000/callback")
	app.base = guarded.URL
	jar, _ := cookiejar.New(nil)
	visitor := &http.Client{Jar: jar} // no account: the demo signs them in
	back := app.allow(visitor, "allow")
	code, tok := app.token(url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "redirect_uri": {app.redirect}, "code_verifier": {app.verifier}})
	access, _ := tok["access_token"].(string)
	if code != http.StatusOK || access == "" {
		t.Fatalf("token on the demo: %d %v", code, tok)
	}
	if role, _, ok := e.whoami(access); !ok || role != store.RoleViewer {
		t.Errorf("demo app reads as %s %v", role, ok)
	}
	if _, page := get(t, visitor, guarded.URL+"/account", nil); strings.Contains(page, "Apps with access") {
		t.Error("the demo lists the apps visitors connected")
	}
	grants, _ := e.st.ListOAuthGrants(ctx, viewer.ID)
	if c, _, _ := post(t, visitor, guarded.URL+"/account/apps/"+grants[0].ID+"/revoke", nil); c != http.StatusForbidden {
		t.Errorf("a visitor revoked another's app: %d", c)
	}
}

// Admins see every app reading the workspace, whose it is, and can revoke it; one address registers only so
// many apps an hour.
func TestOAuthAdminsAndRegistrationLimit(t *testing.T) {
	e := newUIEnv(t)
	var lastClient string
	register := func() int {
		resp, err := http.Post(e.srv.URL+"/oauth/register", "application/json", //nolint:noctx // test
			strings.NewReader(`{"client_name":"Cursor","redirect_uris":["http://127.0.0.1/cb"]}`))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		_ = resp.Body.Close()
		if id, _ := out["client_id"].(string); id != "" {
			lastClient = id
		}
		return resp.StatusCode
	}
	if code := register(); code != http.StatusCreated {
		t.Fatalf("register: %d", code)
	}
	app := newOAuthApp(t, e, lastClient, "http://127.0.0.1:9000/cb")
	member := e.as(store.RoleMember)
	back := app.allow(member, "allow")
	if _, tok := app.token(url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "redirect_uri": {app.redirect}, "code_verifier": {app.verifier}}); tok["access_token"] == nil {
		t.Fatalf("token: %v", tok)
	}

	admin := e.as(store.RoleAdmin)
	_, page := get(t, admin, e.srv.URL+"/settings", nil)
	if !strings.Contains(page, "Apps with access") || !strings.Contains(page, "Cursor") || !strings.Contains(page, "member@example.com") {
		t.Fatal("the admin does not see the member's app")
	}
	grants, _ := e.st.ListWorkspaceOAuthGrants(context.Background(), e.ws.ID)
	post(t, member, e.srv.URL+"/settings/apps/"+grants[0].ID+"/revoke", nil)
	if left, _ := e.st.ListWorkspaceOAuthGrants(context.Background(), e.ws.ID); len(left) != 1 {
		t.Fatal("a member revoked through the admins' page")
	}
	post(t, admin, e.srv.URL+"/settings/apps/"+grants[0].ID+"/revoke", nil)
	if left, _ := e.st.ListWorkspaceOAuthGrants(context.Background(), e.ws.ID); len(left) != 0 {
		t.Fatal("the admin could not revoke it")
	}

	// Twenty registrations an hour per address.
	for range 19 {
		if code := register(); code != http.StatusCreated {
			t.Fatalf("within the limit: %d", code)
		}
	}
	if code := register(); code != http.StatusTooManyRequests {
		t.Errorf("over the limit: %d", code)
	}
}
