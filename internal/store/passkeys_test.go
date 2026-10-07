// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPasskeys(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, err := s.EnsureDefaultWorkspace(ctx)
		if err != nil {
			t.Fatal(err)
		}
		u, err := s.CreateUser(ctx, ws.OrgID, "ana@example.com", "Ana", RoleOwner)
		if err != nil {
			t.Fatal(err)
		}
		k, err := s.AddPasskey(ctx, u.ID, "cred-1", []byte(`{"id":"x"}`), "MacBook")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddPasskey(ctx, u.ID, "cred-1", []byte(`{}`), "again"); !errors.Is(err, ErrExists) {
			t.Fatalf("duplicate credential: %v", err)
		}
		if got, _ := s.GetUser(ctx, u.ID); got.Passkeys != 1 {
			t.Fatalf("user passkeys: %d", got.Passkeys)
		}
		if err := s.UsePasskey(ctx, "cred-1", []byte(`{"id":"y"}`)); err != nil {
			t.Fatal(err)
		}
		got, err := s.PasskeyByCredential(ctx, "cred-1")
		if err != nil || string(got.Credential) != `{"id":"y"}` || got.LastUsedAt.IsZero() || got.UserID != u.ID {
			t.Fatalf("used: %+v %v", got, err)
		}
		if err := s.RenamePasskey(ctx, u.ID, k.ID, "Work laptop"); err != nil {
			t.Fatal(err)
		}
		if err := s.RenamePasskey(ctx, "someone-else", k.ID, "x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("rename another's: %v", err)
		}
		keys, _ := s.ListPasskeys(ctx, u.ID)
		if len(keys) != 1 || keys[0].Name != "Work laptop" {
			t.Fatalf("list: %+v", keys)
		}
		if err := s.DeletePasskey(ctx, u.ID, k.ID); err != nil {
			t.Fatal(err)
		}
		if keys, _ := s.ListPasskeys(ctx, u.ID); len(keys) != 0 {
			t.Fatal("not deleted")
		}

		// A ceremony is answered once, and not after it expired.
		if err := s.SaveWebAuthnSession(ctx, "h1", []byte("data"), time.Minute); err != nil {
			t.Fatal(err)
		}
		if d, err := s.TakeWebAuthnSession(ctx, "h1"); err != nil || string(d) != "data" {
			t.Fatalf("take: %q %v", d, err)
		}
		if _, err := s.TakeWebAuthnSession(ctx, "h1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("taken twice: %v", err)
		}
		_ = s.SaveWebAuthnSession(ctx, "h2", []byte("old"), -time.Second)
		if _, err := s.TakeWebAuthnSession(ctx, "h2"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expired: %v", err)
		}
	})
}
