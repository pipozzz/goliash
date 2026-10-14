// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestWeekly(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var hist []store.PostureSample
	for d := range 30 {
		if d == 15 { // a missing day: the reading before it stands in
			continue
		}
		hist = append(hist, store.PostureSample{Day: start.AddDate(0, 0, d).Format(time.DateOnly), Median: d})
	}
	var days []string
	for _, s := range weekly(hist) {
		days = append(days, s.Day)
	}
	want := []string{"2026-08-01", "2026-08-08", "2026-08-15", "2026-08-23", "2026-08-30"}
	if len(days) != len(want) {
		t.Fatalf("weekly %v", days)
	}
	for i := range want {
		if days[i] != want[i] {
			t.Fatalf("weekly %v, want %v", days, want)
		}
	}
}
