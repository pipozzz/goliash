// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"testing"
)

func TestPostureHistory(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		for _, p := range []PostureSample{
			{Day: "2026-10-01", Exposed: 9, Median: 50},
			{Day: "2026-10-02", Exposed: 8, Median: 45},
			{Day: "2026-10-02", Exposed: 7, Median: 40, Behind90: 2}, // the day's last reading wins
		} {
			if err := s.RecordPosture(ctx, sc, p); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.PostureHistory(ctx, sc, "2026-10-01")
		if err != nil || len(got) != 2 || got[0].Day != "2026-10-01" || got[1].Exposed != 7 || got[1].Behind90 != 2 {
			t.Fatalf("history %+v %v", got, err)
		}
		if got, _ := s.PostureHistory(ctx, sc, "2026-10-02"); len(got) != 1 {
			t.Errorf("since: %+v", got)
		}
	})
}
