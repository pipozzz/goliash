// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
)

type harness struct {
	t     *testing.T
	st    *store.Store
	ws    store.Workspace
	auth  *Auth
	srv   *httptest.Server
	mails []string
	mu    sync.Mutex
}

func newHarness(t *testing.T, mail bool) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	h := &harness{t: t, st: st, ws: ws}
	mux := http.NewServeMux()
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)

	var mf MailFunc
	if mail {
		mf = func(_ context.Context, to, subject, body string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.mails = append(h.mails, to+"|"+subject+"|"+body)
			return nil
		}
	}
	h.auth, err = New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), h.srv.URL, mf)
	if err != nil {
		t.Fatal(err)
	}
	h.auth.Routes(mux)
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		p, ok, err := h.auth.Authenticate(r)
		if err != nil || !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(p.Name() + " " + p.Role + " " + p.Via))
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("home")) })
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("login " + r.URL.RawQuery)) })
	return h
}

func (h *harness) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func post(t *testing.T, c *http.Client, u string, form url.Values) string {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestMagicLinkSessionAndLogout(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	u, err := h.st.CreateUser(ctx, h.ws.OrgID, "Ana@Example.com", "Ana", store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMembership(ctx, u.ID, h.ws.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	c := h.client()

	// Asking for a link answers the same for known and unknown addresses.
	if got := post(t, c, h.srv.URL+"/auth/magic", url.Values{"email": {"ana@example.com"}}); got != "login sent=1" {
		t.Fatalf("known: %q", got)
	}
	if got := post(t, c, h.srv.URL+"/auth/magic", url.Values{"email": {"nobody@example.com"}}); got != "login sent=1" {
		t.Fatalf("unknown: %q", got)
	}
	if len(h.mails) != 1 || !strings.HasPrefix(h.mails[0], "ana@example.com|Your Goliash sign-in link|") {
		t.Fatalf("mails %v", h.mails)
	}
	link := h.mails[0][strings.Index(h.mails[0], h.srv.URL):]
	link = link[:strings.Index(link, "\r\n")]

	if code, body := get(t, c, h.srv.URL+"/whoami"); code != http.StatusUnauthorized {
		t.Fatalf("signed in before using the link: %d %s", code, body)
	}
	if _, body := get(t, c, link); body != "home" {
		t.Fatalf("link did not sign in: %s", body)
	}
	if _, body := get(t, c, h.srv.URL+"/whoami"); body != "ana@example.com member session" {
		t.Fatalf("whoami %q", body)
	}
	got, _ := h.st.GetUser(ctx, u.ID)
	if got.LastLoginAt.IsZero() {
		t.Fatal("last login not recorded")
	}

	// A link works once.
	if _, body := get(t, h.client(), link); body != "login error=link" {
		t.Fatalf("reused link: %s", body)
	}

	post(t, c, h.srv.URL+"/auth/logout", nil)
	if code, _ := get(t, c, h.srv.URL+"/whoami"); code != http.StatusUnauthorized {
		t.Fatal("still signed in after logout")
	}
}

func TestAPIToken(t *testing.T) {
	h := newHarness(t, false)
	tok, hash := tokens.New(tokens.API)
	if _, err := h.st.CreateAPIToken(context.Background(), h.ws.Scope(), store.APIToken{Name: "prometheus", Role: store.RoleMember}, hash); err != nil {
		t.Fatal(err)
	}
	call := func(token string) (int, string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, h.srv.URL+"/whoami", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := call(tok); code != 200 || body != "api token prometheus member token" {
		t.Fatalf("api token: %d %s", code, body)
	}
	ctx := context.Background()
	viewer, vh := tokens.New(tokens.API)
	_, _ = h.st.CreateAPIToken(ctx, h.ws.Scope(), store.APIToken{Name: "grafana"}, vh)
	if code, body := call(viewer); code != 200 || body != "api token grafana viewer token" {
		t.Fatalf("viewer token: %d %s", code, body)
	}
	expired, eh := tokens.New(tokens.API)
	_, _ = h.st.CreateAPIToken(ctx, h.ws.Scope(), store.APIToken{Name: "old", ExpiresAt: time.Now().Add(-time.Minute)}, eh)
	revoked, rh := tokens.New(tokens.API)
	rt, _ := h.st.CreateAPIToken(ctx, h.ws.Scope(), store.APIToken{Name: "gone", ExpiresAt: time.Now().Add(time.Hour)}, rh)
	if code, _ := call(revoked); code != 200 {
		t.Fatal("token with a future expiry rejected")
	}
	if err := h.st.RevokeAPIToken(ctx, h.ws.Scope(), rt.ID); err != nil {
		t.Fatal(err)
	}
	agentTok, _ := tokens.New(tokens.Agent)
	other, _ := tokens.New(tokens.API)
	for _, bad := range []string{agentTok, other, "nonsense", expired, revoked} {
		if code, _ := call(bad); code != http.StatusUnauthorized {
			t.Fatalf("%q accepted", bad)
		}
	}
}

// fakeProvider is a minimal OpenID Connect provider.
type fakeProvider struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	email  string
	verify bool
	nonces map[string]string // code -> nonce
	mu     sync.Mutex
}

func newFakeProvider(t *testing.T) *fakeProvider {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := &fakeProvider{key: key, verify: true, nonces: map[string]string{}}
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/authorize", "token_endpoint": p.srv.URL + "/token",
			"jwks_uri": p.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			t.Errorf("no PKCE: %s", r.URL.RawQuery)
		}
		code := "code-" + q.Get("state")[:8]
		p.mu.Lock()
		p.nonces[code] = q.Get("nonce")
		p.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code_verifier") == "" {
			t.Errorf("no code_verifier")
		}
		p.mu.Lock()
		nonce := p.nonces[r.Form.Get("code")]
		email, verified := p.email, p.verify
		p.mu.Unlock()
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: "k1"}}, nil)
		claims := map[string]any{
			"iss": p.srv.URL, "sub": "user-1", "aud": "goliash", "exp": time.Now().Add(time.Hour).Unix(),
			"iat": time.Now().Unix(), "nonce": nonce, "email": email, "email_verified": verified, "name": "Test User",
		}
		raw, _ := jwt.Signed(signer).Claims(claims).Serialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": raw, "expires_in": 3600})
	})
	return p
}

func TestOIDC(t *testing.T) {
	h := newHarness(t, false)
	p := newFakeProvider(t)
	o, err := NewOIDC(context.Background(), OIDCConfig{Issuer: p.srv.URL, ClientID: "goliash", ClientSecret: "s", Domains: []string{"acme.io"}}, h.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	h.auth.SetOIDC(o)
	_, _ = h.st.CreateUser(context.Background(), h.ws.OrgID, "boss@elsewhere.org", "", store.RoleAdmin)

	cases := []struct {
		email    string
		verified bool
		want     string
	}{
		{"boss@elsewhere.org", true, "boss@elsewhere.org admin session"}, // existing user
		{"new@acme.io", true, "new@acme.io viewer session"},              // allowed domain, created as viewer
		{"stranger@evil.com", true, ""},                                  // not invited
		{"new2@acme.io", false, ""},                                      // e-mail not verified
	}
	for _, c := range cases {
		p.mu.Lock()
		p.email, p.verify = c.email, c.verified
		p.mu.Unlock()
		cl := h.client()
		_, body := get(t, cl, h.srv.URL+"/auth/oidc/start")
		_, who := get(t, cl, h.srv.URL+"/whoami")
		if c.want == "" {
			if body != "login error=oidc" || who != "" {
				t.Errorf("%s: should be refused, got %q / %q", c.email, body, who)
			}
			continue
		}
		if body != "home" || who != c.want {
			t.Errorf("%s: got %q / %q", c.email, body, who)
		}
	}

	// A callback without the state cookie (CSRF / replay) is refused.
	if _, body := get(t, h.client(), h.srv.URL+"/auth/oidc/callback?code=x&state=y"); body != "login error=oidc" {
		t.Fatalf("callback without state: %q", body)
	}
}

func TestWorkspaceSelection(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	other, _ := h.st.CreateWorkspace(ctx, h.ws.OrgID, "Client A", "client-a")
	secret, _ := h.st.CreateWorkspace(ctx, h.ws.OrgID, "Client B", "client-b")
	u, _ := h.st.CreateUser(ctx, h.ws.OrgID, "ops@client-a.example", "", store.RoleViewer)
	_ = h.st.SetMembership(ctx, u.ID, other.ID, store.RoleMember)

	link, _ := h.auth.LoginLink(ctx, u)
	c := h.client()
	get(t, c, link)

	whoami := func(cookie string) Principal {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		for _, ck := range c.Jar.Cookies(mustURL(h.srv.URL)) {
			req.AddCookie(ck)
		}
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: WorkspaceCookie, Value: cookie})
		}
		p, ok, err := h.auth.Authenticate(req)
		if err != nil || !ok {
			t.Fatalf("authenticate: %v %v", ok, err)
		}
		return p
	}
	p := whoami("")
	if p.Scope.WorkspaceID != other.ID || p.Role != store.RoleMember || len(p.Workspaces) != 1 || p.OrgWide() {
		t.Fatalf("default workspace %+v", p)
	}
	// A forged cookie for a workspace the user may not open is ignored.
	if p := whoami(secret.ID); p.Scope.WorkspaceID != other.ID {
		t.Fatalf("forged workspace cookie honoured: %s", p.Scope.WorkspaceID)
	}

	// Org admins reach every workspace and may pick one.
	admin, _ := h.st.CreateUser(ctx, h.ws.OrgID, "admin@msp.example", "", store.RoleAdmin)
	link, _ = h.auth.LoginLink(ctx, admin)
	c = h.client()
	get(t, c, link)
	if p := whoami(secret.ID); p.Scope.WorkspaceID != secret.ID || p.Role != store.RoleAdmin || len(p.Workspaces) != 3 || !p.OrgWide() {
		t.Fatalf("admin %+v", p)
	}
}

func mustURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}
