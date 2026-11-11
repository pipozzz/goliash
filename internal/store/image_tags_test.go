// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"testing"
	"time"
)

// A repository's push times are replaced as a whole and listed newest first.
func TestTagTimes(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
		repo := "240220093055.dkr.ecr.us-west-2.amazonaws.com/chat"
		if err := s.SetTagTimes(ctx, sc, repo, map[string]time.Time{"old": day(1), "bca73851": day(3)}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTagTimes(ctx, sc, repo, map[string]time.Time{"f00d1e5": day(9), "bca73851": day(3)}); err != nil {
			t.Fatal(err)
		}
		got, err := s.TagTimes(ctx, sc, repo)
		if err != nil || len(got) != 2 || got[0].Tag != "f00d1e5" || got[1].Tag != "bca73851" || !got[0].PushedAt.Equal(day(9)) {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

// Tags whose image a lifecycle policy deletes are listed up to a time, with their repository.
func TestExpiringTags(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		repo := "240220093055.dkr.ecr.us-west-2.amazonaws.com/chat"
		if err := s.SetTagFacts(ctx, sc, repo, []TagTime{
			{Tag: "bca73851", PushedAt: now.AddDate(0, 0, -8), Digest: "sha256:b", ExpiresAt: now.AddDate(0, 0, 3)},
			{Tag: "f00d1e5", PushedAt: now.AddDate(0, 0, -1), Digest: "sha256:a"},
			{Tag: "late", PushedAt: now.AddDate(0, 0, -2), ExpiresAt: now.AddDate(0, 0, 30)},
		}); err != nil {
			t.Fatal(err)
		}
		got, err := s.ExpiringTags(ctx, sc, now.AddDate(0, 0, 7))
		if err != nil || len(got) != 1 || got[0].Tag != "bca73851" || got[0].Repo != repo || got[0].Digest != "sha256:b" {
			t.Fatalf("%+v %v", got, err)
		}
	})
}
