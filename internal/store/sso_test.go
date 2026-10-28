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

func TestSSOSettings(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, err := s.EnsureDefaultWorkspace(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetSSO(ctx, ws.OrgID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("none yet: %v", err)
		}
		v := SSOSettings{OrgID: ws.OrgID, Issuer: "https://idp.example", ClientID: "goliash", ClientSecret: "s3cret", GroupRoles: "ops=admin", UpdatedBy: "ana"}
		if err := s.SetSSO(ctx, v); !errors.Is(err, ErrSSOUnencrypted) {
			t.Fatalf("stored without a key: %v", err)
		}
		if err := s.SetSecretKey(bytes.Repeat([]byte{7}, SecretKeySize)); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSSO(ctx, v); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := s.queryRow(ctx, s.db, `SELECT client_secret FROM sso_settings WHERE org_id = ?`, ws.OrgID).Scan(&raw); err != nil || strings.Contains(raw, "s3cret") {
			t.Fatalf("secret at rest %q %v", raw, err)
		}
		v.Name = "Acme"
		if err := s.SetSSO(ctx, v); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSSO(ctx, ws.OrgID)
		if err != nil || got.ClientSecret != "s3cret" || got.Name != "Acme" || got.GroupRoles != "ops=admin" || got.UpdatedBy != "ana" {
			t.Fatalf("got %+v %v", got, err)
		}
		if err := s.DeleteSSO(ctx, ws.OrgID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetSSO(ctx, ws.OrgID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted: %v", err)
		}
	})
}
