// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestHashAndVerifyPassword(t *testing.T) {
	h := HashPassword("correct horse battery")
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash %q", h)
	}
	if h == HashPassword("correct horse battery") {
		t.Fatal("same salt twice")
	}
	if !VerifyPassword("correct horse battery", h) {
		t.Fatal("right password rejected")
	}
	for _, bad := range []string{"correct horse batterY", "", strings.Repeat("x", 300)} {
		if VerifyPassword(bad, h) {
			t.Fatalf("%q accepted", bad)
		}
	}
	for _, broken := range []string{
		"", "plain", "$argon2i$v=19$m=1,t=1,p=1$AA$AA", "$argon2id$v=19$m=99999999,t=2,p=1$AA$AA",
		strings.Replace(h, "v=19", "v=16", 1),
	} {
		if VerifyPassword("correct horse battery", broken) {
			t.Fatalf("broken hash %q accepted", broken)
		}
	}
}

func TestCheckPassword(t *testing.T) {
	for pw, ok := range map[string]bool{
		"short":                   false,
		"aaaaaaaaaaaaaaaa":        false,
		"ana@example.com":         false,
		"ANA@example.com":         false,
		"correct horse staple":    true,
		strings.Repeat("ab", 129): false,
	} {
		err := CheckPassword(pw, "ana@example.com")
		if (err == nil) != ok || (err != nil && !errors.Is(err, ErrWeakPassword)) {
			t.Errorf("%q: %v", pw, err)
		}
	}
}

func TestLimiter(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Now()
	l := newLimiter(st, "test", 3, 15*time.Minute)
	other := newLimiter(st, "other", 3, 15*time.Minute) // another server's view of the same database
	other.scope = "test"
	l.now = func() time.Time { return now }
	for range 3 {
		if l.blocked(ctx, "k") {
			t.Fatal("blocked too early")
		}
		other.fail(ctx, "k") // failures on any server count
	}
	if !l.blocked(ctx, "k") || l.blocked(ctx, "other-key") {
		t.Fatal("limit not per key")
	}
	if newLimiter(st, "another-scope", 3, time.Hour).blocked(ctx, "k") {
		t.Fatal("scopes share keys")
	}
	now = now.Add(16 * time.Minute)
	if l.blocked(ctx, "k") {
		t.Fatal("window did not slide")
	}
	l.fail(ctx, "k")
	l.reset(ctx, "k")
	if l.recentCount(ctx, "k") != 0 {
		t.Fatal("not forgotten")
	}
}

func TestPasswordLogin(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	u, _ := h.st.CreateUser(ctx, h.ws.OrgID, "ana@example.com", "", store.RoleMember)
	_ = h.st.SetMembership(ctx, u.ID, h.ws.ID, store.RoleMember)
	nopw, _ := h.st.CreateUser(ctx, h.ws.OrgID, "bob@example.com", "", store.RoleMember)
	_ = h.st.SetMembership(ctx, nopw.ID, h.ws.ID, store.RoleMember)
	if err := h.st.SetUserPassword(ctx, u.ID, HashPassword("correct horse staple"), ""); err != nil {
		t.Fatal(err)
	}
	login := func(c *http.Client, email, pw string) string {
		return post(t, c, h.srv.URL+"/auth/password", url.Values{"email": {email}, "password": {pw}})
	}

	// Wrong password, unknown address and a user without a password answer alike.
	for _, try := range [][2]string{{"ana@example.com", "wrong password!"}, {"nobody@example.com", "x"}, {"bob@example.com", ""}} {
		if got := login(h.client(), try[0], try[1]); got != "login error=password" {
			t.Fatalf("%v: %q", try, got)
		}
	}

	c := h.client()
	if got := login(c, " Ana@Example.com ", "correct horse staple"); got != "home" {
		t.Fatalf("sign in: %q", got)
	}
	if _, body := get(t, c, h.srv.URL+"/whoami"); body != "ana@example.com member session" {
		t.Fatalf("whoami %q", body)
	}
	sessions, _ := h.st.ListSessions(ctx, u.ID)
	if len(sessions) != 1 || sessions[0].Method != "password" || sessions[0].IP != "127.0.0.1" || !strings.HasPrefix(sessions[0].UserAgent, "Go-http-client") {
		t.Fatalf("session %+v", sessions)
	}

	// Eight failures for an address pause it, even with the right password.
	for range 8 {
		login(h.client(), "ana@example.com", "wrong password!")
	}
	if got := login(h.client(), "ana@example.com", "correct horse staple"); got != "login error=throttled" {
		t.Fatalf("not throttled: %q", got)
	}

	h.auth.SetPasswordLogin(false)
	if got := login(h.client(), "bob@example.com", "x"); got != "login " {
		t.Fatalf("password login while off: %q", got)
	}
}

func TestClientIP(t *testing.T) {
	a := &Auth{}
	r, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.5:4321"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := a.ClientIP(r); got != "10.0.0.5" {
		t.Fatalf("untrusted proxy header used: %s", got)
	}
	a.SetTrustProxy(true)
	if got := a.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("trusted proxy: %s", got)
	}
}
