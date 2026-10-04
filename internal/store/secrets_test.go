// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestChannelSecretsAtRest(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		sc := ws.Scope()
		cfg := json.RawMessage(`{"url":"https://hooks.slack.com/services/T0/B0/secret"}`)

		// A channel from before the key, then the key: SealSecrets encrypts it.
		legacy, err := s.CreateChannel(ctx, Channel{Scope: sc, Type: "slack", Name: "legacy", Config: cfg})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetSecretKey(bytes.Repeat([]byte{7}, SecretKeySize)); err != nil {
			t.Fatal(err)
		}
		if n, err := s.SealSecrets(ctx); err != nil || n != 1 {
			t.Fatalf("sealed %d, %v", n, err)
		}
		if n, _ := s.SealSecrets(ctx); n != 0 {
			t.Fatal("sealed twice")
		}
		if _, err := s.CreateChannel(ctx, Channel{Scope: sc, Type: "webhook", Name: "new", Config: json.RawMessage(`{"url":"https://example.com/h","secret":"s3"}`)}); err != nil {
			t.Fatal(err)
		}

		var raw string
		if err := s.queryRow(ctx, s.db, `SELECT config FROM notification_channels WHERE id = ?`, legacy.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := sealed(raw); !ok || strings.Contains(raw, "hooks.slack.com") {
			t.Fatalf("stored in the clear: %s", raw)
		}
		chans, err := s.ListChannels(ctx, sc)
		if err != nil || len(chans) != 2 {
			t.Fatalf("channels %+v %v", chans, err)
		}
		var got struct{ URL, Secret string }
		_ = json.Unmarshal(chans[0].Config, &got)
		if err != nil || len(chans) != 2 || got.URL != "https://hooks.slack.com/services/T0/B0/secret" || !strings.Contains(string(chans[1].Config), `"secret":"s3"`) {
			t.Fatalf("channels %+v %v", chans, err)
		}
		if err := s.CheckSecrets(ctx); err != nil {
			t.Fatal(err)
		}

		// A ciphertext moved to another row does not open.
		if _, err := s.exec(ctx, s.db, `UPDATE notification_channels SET config = ? WHERE name = 'new'`, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ListChannels(ctx, sc); err == nil {
			t.Fatal("ciphertext opened in another row")
		}

		// Wrong key and no key are caught at start.
		_ = s.SetSecretKey(bytes.Repeat([]byte{8}, SecretKeySize))
		if err := s.CheckSecrets(ctx); err == nil {
			t.Fatal("wrong key accepted")
		}
		s.aead = nil
		if err := s.CheckSecrets(ctx); !errors.Is(err, ErrNoSecretKey) {
			t.Fatalf("missing key: %v", err)
		}
		if err := s.SetSecretKey([]byte("short")); err == nil {
			t.Fatal("short key accepted")
		}
	})
}

func TestChannelTypes(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		for _, typ := range ChannelTypes {
			if _, err := s.CreateChannel(ctx, Channel{Scope: ws.Scope(), Type: typ, Name: "ch-" + typ, Config: json.RawMessage(`{}`)}); err != nil {
				t.Errorf("%s: %v", typ, err)
			}
		}
		if _, err := s.CreateChannel(ctx, Channel{Scope: ws.Scope(), Type: "pager", Name: "x", Config: json.RawMessage(`{}`)}); err == nil {
			t.Fatal("unknown channel type accepted")
		}
	})
}

func TestDriftKinds(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		svc, _ := s.EnsureService(ctx, f.ws.Scope(), "web")
		for _, kind := range DriftKinds {
			if _, err := s.OpenDrift(ctx, Drift{Scope: f.ws.Scope(), ServiceID: svc.ID, EnvironmentID: f.env.ID, Kind: kind, Detail: json.RawMessage(`{}`)}); err != nil {
				t.Errorf("%s: %v", kind, err)
			}
		}
		if _, err := s.OpenDrift(ctx, Drift{Scope: f.ws.Scope(), ServiceID: svc.ID, EnvironmentID: f.env.ID, Kind: "bogus"}); err == nil {
			t.Fatal("unknown drift kind accepted")
		}
	})
}

// A version that comes back reuses its instance row; the history still knows when it
// did not run.
func TestInstancesAt(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		apply := func(at time.Time, tag string) {
			t.Helper()
			current, _ := s.ListTargetInstances(ctx, sc, f.tgt.ID)
			ch := SnapshotChanges{Scope: sc, TargetID: f.tgt.ID, SnapshotID: NewID(), At: at, Upsert: []Instance{{
				TargetID: f.tgt.ID, EnvironmentID: f.env.ID, WorkloadID: "w", WorkloadKind: "deployment", WorkloadName: "web",
				ContainerName: "app", Image: "nginx:" + tag, Tag: tag, Running: 2, IsMain: true,
			}}}
			for _, i := range current {
				if i.Active() && i.Tag != tag {
					ch.Remove = append(ch.Remove, i.ID)
				}
			}
			_, _ = s.InsertSnapshot(ctx, Snapshot{ID: ch.SnapshotID, Scope: sc, TargetID: f.tgt.ID, CollectedAt: at, Complete: true, Payload: json.RawMessage(`{}`)})
			if err := s.ApplySnapshot(ctx, ch); err != nil {
				t.Fatal(err)
			}
		}
		apply(t0, "1.0")
		apply(t0.Add(time.Hour), "1.0") // unchanged: no new period
		apply(t0.Add(2*time.Hour), "2.0")
		apply(t0.Add(4*time.Hour), "1.0") // back again
		tagAt := func(at time.Time) string {
			t.Helper()
			is, err := s.InstancesAt(ctx, sc, at)
			if err != nil {
				t.Fatal(err)
			}
			var tags []string
			for _, i := range is {
				tags = append(tags, i.Tag)
			}
			return strings.Join(tags, ",")
		}
		for at, want := range map[time.Time]string{
			t0.Add(-time.Minute): "", t0.Add(30 * time.Minute): "1.0", t0.Add(3 * time.Hour): "2.0", t0.Add(5 * time.Hour): "1.0",
		} {
			if got := tagAt(at); got != want {
				t.Errorf("at %s: %q, want %q", at.Format("15:04"), got, want)
			}
		}
	})
}
