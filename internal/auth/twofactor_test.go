// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestTwoFactorSignIn(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	u, _ := h.st.CreateUser(ctx, h.ws.OrgID, "ana@example.com", "", store.RoleMember)
	_ = h.st.SetMembership(ctx, u.ID, h.ws.ID, store.RoleMember)
	_ = h.st.SetUserPassword(ctx, u.ID, HashPassword("correct horse staple"), "")
	secret := NewTOTPSecret()
	_ = h.st.StartTOTP(ctx, u.ID, secret)
	sum := sha256.Sum256([]byte(NormalizeRecoveryCode("aaaa-bbbb-cccc")))
	if err := h.st.EnableTOTP(ctx, u.ID, 0, []string{hex.EncodeToString(sum[:])}); err != nil {
		t.Fatal(err)
	}

	browser := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	postTo := func(c *http.Client, path string, form url.Values) (int, string) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.srv.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location")
	}
	signedIn := func(c *http.Client) bool {
		code, _ := get(t, c, h.srv.URL+"/whoami")
		return code == http.StatusOK
	}
	now, _ := TOTPCode(secret, time.Now())

	// A password alone does not sign in: it leads to the code step.
	c := browser()
	if _, loc := postTo(c, "/auth/password", url.Values{"email": {"ana@example.com"}, "password": {"correct horse staple"}}); loc != "/login/2fa" {
		t.Fatalf("after password: %q", loc)
	}
	if signedIn(c) {
		t.Fatal("signed in without the second factor")
	}
	if p, ok := h.auth.SecondFactorPending(&http.Request{Header: http.Header{"Cookie": {cookieHeader(c, h.srv.URL)}}}); !ok || p.ID != u.ID {
		t.Fatal("pending second factor not found")
	}
	if _, loc := postTo(c, "/auth/2fa", url.Values{"code": {"000000"}}); loc != "/login/2fa?error=code" {
		t.Fatalf("wrong code: %q", loc)
	}
	if _, loc := postTo(c, "/auth/2fa", url.Values{"code": {now}}); loc != "/" || !signedIn(c) {
		t.Fatalf("right code: %q", loc)
	}
	sessions, _ := h.st.ListSessions(ctx, u.ID)
	if len(sessions) != 1 || sessions[0].Method != "password+totp" {
		t.Fatalf("session %+v", sessions)
	}

	// The same code does not work twice.
	c2 := browser()
	postTo(c2, "/auth/password", url.Values{"email": {"ana@example.com"}, "password": {"correct horse staple"}})
	if _, loc := postTo(c2, "/auth/2fa", url.Values{"code": {now}}); loc != "/login/2fa?error=code" {
		t.Fatalf("replayed code: %q", loc)
	}
	// A recovery code works once.
	if _, loc := postTo(c2, "/auth/2fa", url.Values{"recovery": {"AAAA BBBB CCCC"}}); loc != "/" || !signedIn(c2) {
		t.Fatalf("recovery code: %q", loc)
	}
	c3 := browser()
	postTo(c3, "/auth/password", url.Values{"email": {"ana@example.com"}, "password": {"correct horse staple"}})
	if _, loc := postTo(c3, "/auth/2fa", url.Values{"recovery": {"aaaa-bbbb-cccc"}}); loc != "/login/2fa?error=code" {
		t.Fatalf("recovery code reused: %q", loc)
	}

	// A sign-in link also asks for the code; a recovery link does not.
	link, _ := h.auth.LoginLink(ctx, u)
	c4 := browser()
	if code, _ := get(t, c4, link); code != http.StatusSeeOther || signedIn(c4) {
		t.Fatalf("link skipped the second factor: %d", code)
	}
	rec, _ := h.auth.RecoveryLink(ctx, u)
	c5 := browser()
	get(t, c5, rec)
	if !signedIn(c5) {
		t.Fatal("recovery link did not sign in")
	}

	// Five wrong codes for a person (counted across attempts: one was wrong above) end
	// the attempt; starting over does not reset the count.
	c6 := browser()
	postTo(c6, "/auth/password", url.Values{"email": {"ana@example.com"}, "password": {"correct horse staple"}})
	var loc string
	tries := 0
	for loc != "/login?error=throttled" && tries < 5 {
		_, loc = postTo(c6, "/auth/2fa", url.Values{"code": {"111111"}})
		tries++
	}
	if loc != "/login?error=throttled" || tries != 4 {
		t.Fatalf("after %d wrong codes: %q", tries, loc)
	}
	next, _ := TOTPCode(secret, time.Now().Add(30*time.Second))
	if _, loc = postTo(c6, "/auth/2fa", url.Values{"code": {next}}); loc != "/login?error=expired" {
		t.Fatalf("attempt still open: %q", loc)
	}
}

func cookieHeader(c *http.Client, base string) string {
	u, _ := url.Parse(base)
	var parts []string
	for _, ck := range c.Jar.Cookies(u) {
		parts = append(parts, ck.Name+"="+ck.Value)
	}
	return strings.Join(parts, "; ")
}
