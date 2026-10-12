// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRegistryCredentials(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		sc := ws.Scope()
		other, err := s.CreateWorkspace(ctx, ws.OrgID, "Other", "other")
		if err != nil {
			t.Fatal(err)
		}
		c := RegistryCredential{Scope: sc, Host: "registry.example.com", Username: "robot", Secret: "s3cret-token", CreatedBy: "ana"}

		// Without a key nothing is stored in the clear.
		if _, err := s.SetRegistryCredential(ctx, c); !errors.Is(err, ErrSecretsUnencrypted) {
			t.Fatalf("stored without a key: %v", err)
		}
		if err := s.SetSecretKey(bytes.Repeat([]byte{9}, SecretKeySize)); err != nil {
			t.Fatal(err)
		}
		saved, err := s.SetRegistryCredential(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := s.queryRow(ctx, s.db, `SELECT secret FROM registry_credentials WHERE id = ?`, saved.ID).Scan(&raw); err != nil || strings.Contains(raw, "s3cret") {
			t.Fatalf("secret at rest %q %v", raw, err)
		}
		if err := s.CheckSecrets(ctx); err != nil {
			t.Fatal(err)
		}

		// Saving again replaces; the use is recorded; other workspaces see nothing.
		c.Secret = "rotated"
		if _, err := s.SetRegistryCredential(ctx, c); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordRegistryUse(ctx, sc, c.Host, "refused"); err != nil {
			t.Fatal(err)
		}
		all, err := s.ListRegistryCredentials(ctx, sc)
		if err != nil || len(all) != 1 || all[0].Secret != "rotated" || all[0].Username != "robot" || all[0].Error != "refused" || !all[0].UsedAt.IsZero() {
			t.Fatalf("credentials %+v %v", all, err)
		}
		_ = s.RecordRegistryUse(ctx, sc, c.Host, "")
		if all, _ = s.ListRegistryCredentials(ctx, sc); all[0].Error != "" || all[0].UsedAt.IsZero() {
			t.Fatalf("use not recorded %+v", all[0])
		}
		if theirs, _ := s.ListRegistryCredentials(ctx, other.Scope()); len(theirs) != 0 {
			t.Fatal("credentials leak into another workspace")
		}
		if _, err := s.DeleteRegistryCredential(ctx, other.Scope(), all[0].ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("another workspace deleted them")
		}

		// A different key cannot open them, and the server says so at start.
		if err := s.SetSecretKey(bytes.Repeat([]byte{8}, SecretKeySize)); err != nil {
			t.Fatal(err)
		}
		if err := s.CheckSecrets(ctx); err == nil {
			t.Fatal("wrong key accepted")
		}
		_ = s.SetSecretKey(bytes.Repeat([]byte{9}, SecretKeySize))
		if host, err := s.DeleteRegistryCredential(ctx, sc, all[0].ID); err != nil || host != c.Host {
			t.Fatalf("delete %q %v", host, err)
		}
		if all, _ = s.ListRegistryCredentials(ctx, sc); len(all) != 0 {
			t.Fatal("not deleted")
		}
	})
}
