// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOAuthStore(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		u, err := s.CreateUser(ctx, f.ws.OrgID, "app-user@example.com", "", RoleMember)
		if err != nil {
			t.Fatal(err)
		}
		c := OAuthClient{ID: "glsh_cli_x", Name: "Claude Code", RedirectURIs: []string{"http://localhost/cb"}}
		if err := s.SaveOAuthClient(ctx, c); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetOAuthClient(ctx, c.ID); err != nil || got.Name != "Claude Code" || len(got.RedirectURIs) != 1 {
			t.Fatalf("client %+v %v", got, err)
		}

		// A code works once, and not after it expires.
		code := OAuthCode{
			Hash: "h1", ClientID: c.ID, UserID: u.ID, WorkspaceID: f.ws.ID, RedirectURI: "http://localhost/cb",
			Challenge: "ch", Scope: "goliash:read", Resource: "https://g/mcp", ExpiresAt: time.Now().Add(time.Minute),
		}
		if err := s.CreateOAuthCode(ctx, code); err != nil {
			t.Fatal(err)
		}
		if got, err := s.ConsumeOAuthCode(ctx, "h1"); err != nil || got.UserID != u.ID || got.Challenge != "ch" {
			t.Fatalf("code %+v %v", got, err)
		}
		if _, err := s.ConsumeOAuthCode(ctx, "h1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("a code twice: %v", err)
		}
		code.Hash, code.ExpiresAt = "h2", time.Now().Add(-time.Second)
		_ = s.CreateOAuthCode(ctx, code)
		if _, err := s.ConsumeOAuthCode(ctx, "h2"); !errors.Is(err, ErrNotFound) {
			t.Errorf("an expired code: %v", err)
		}

		// A grant: its access token reads; rotating replaces both tokens, for its client only.
		g, err := s.CreateOAuthGrant(ctx, OAuthGrant{
			UserID: u.ID, WorkspaceID: f.ws.ID, ClientID: c.ID, ClientName: c.Name,
			Scope: "goliash:read", Resource: "https://g/mcp", AccessHash: "a1", AccessExpiresAt: time.Now().Add(time.Hour),
			RefreshHash: "r1", RefreshExpiresAt: time.Now().Add(24 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := s.OAuthAccess(ctx, "a1"); err != nil || got.ID != g.ID || got.Workspace == "" {
			t.Fatalf("access %+v %v", got, err)
		}
		next := OAuthGrant{AccessHash: "a2", AccessExpiresAt: time.Now().Add(time.Hour), RefreshHash: "r2", RefreshExpiresAt: time.Now().Add(24 * time.Hour)}
		if _, err := s.RotateOAuthGrant(ctx, "r1", "someone-else", next); !errors.Is(err, ErrNotFound) {
			t.Errorf("rotated for another client: %v", err)
		}
		if _, err := s.RotateOAuthGrant(ctx, "r1", c.ID, next); err != nil {
			t.Fatal(err)
		}
		if _, err := s.OAuthAccess(ctx, "a1"); !errors.Is(err, ErrNotFound) {
			t.Error("the old access token still reads")
		}
		if _, err := s.RotateOAuthGrant(ctx, "r1", c.ID, next); !errors.Is(err, ErrNotFound) {
			t.Error("a refresh token rotated twice")
		}
		if grants, _ := s.ListOAuthGrants(ctx, u.ID); len(grants) != 1 {
			t.Fatalf("grants %d", len(grants))
		}
		if err := s.RevokeOAuthToken(ctx, "r2", c.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.OAuthAccess(ctx, "a2"); !errors.Is(err, ErrNotFound) {
			t.Error("a revoked grant still reads")
		}
		// Housekeeping keeps a client in use; nothing else here is old enough to go.
		if _, err := s.Housekeep(ctx, 1); err != nil {
			t.Fatal(err)
		}
	})
}
