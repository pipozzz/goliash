// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A signed-in CLI uses its token, renews it when it runs out, keeps the file private, and prefers an explicit
// token.
func TestSignedInToken(t *testing.T) {
	var refreshed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "glsh_ort_old" || r.PostForm.Get("client_id") != "glsh_cli_x" { //nolint:gosec // test values
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		refreshed.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "glsh_oat_new", "refresh_token": "glsh_ort_new", "expires_in": 3600}) //nolint:gosec // test values
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "goliash", "credentials.json")
	t.Setenv("GOLIASH_CREDENTIALS", path)
	t.Setenv("GOLIASH_URL", "")
	if tokenFor("https://g.example", "") != nil {
		t.Fatal("a token without signing in")
	}
	if err := saveCredentials(credentials{Default: "https://g.example", Servers: map[string]*credential{"https://g.example": { //nolint:gosec // test values
		ClientID: "glsh_cli_x", AccessToken: "glsh_oat_old", RefreshToken: "glsh_ort_old", //nolint:gosec // test values
		ExpiresAt: time.Now().Add(-time.Minute), TokenURL: srv.URL,
	}}}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file: %v %v", fi.Mode(), err)
	}
	if defaultServer() != "https://g.example" {
		t.Errorf("default server %q", defaultServer())
	}
	tok := tokenFor("https://g.example/", "")
	for range 2 { // renewed once, then the new one is used
		if got, err := tok(context.Background()); err != nil || got != "glsh_oat_new" {
			t.Fatalf("token %q %v", got, err)
		}
	}
	if refreshed.Load() != 1 {
		t.Errorf("refreshed %d times", refreshed.Load())
	}
	if c, _ := loadCredentials(); c.Servers["https://g.example"].RefreshToken != "glsh_ort_new" {
		t.Error("the rotated refresh token was not kept")
	}
	if got, _ := tokenFor("https://g.example", "glsh_api_flag")(context.Background()); got != "glsh_api_flag" {
		t.Errorf("an explicit token loses to the saved one: %s", got)
	}
}
