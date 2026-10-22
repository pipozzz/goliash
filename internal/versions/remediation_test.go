// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestWithDeadlines(t *testing.T) {
	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	now := day("2026-10-22").Add(15 * time.Hour)
	r := store.Remediation{Exploited: 7, Security: 14, EOL: 30}
	us := WithDeadlines([]Update{
		{Exploited: []string{"CVE-1"}, ExploitedSince: day("2026-10-10"), SecurityFix: "security", SecuritySince: day("2026-10-01")},
		{SecurityFix: "CVE-2", SecuritySince: day("2026-10-20")},
		{EOL: "2026-09-01", EOLPassed: true},
		{EOL: "2026-12-01"},
		{Jump: JumpMajor},
	}, r)
	for i, want := range []struct {
		due, why string
		left     int
	}{
		{"2026-10-15", "security fix", -7}, // the security fix's deadline came first
		{"2026-11-03", "security fix", 12},
		{"2026-10-01", "end of life", -21},
		{"", "", 0},
		{"", "", 0},
	} {
		u := us[i]
		got := ""
		if !u.Due.IsZero() {
			got = u.Due.Format(time.DateOnly)
		}
		if got != want.due || u.DueFor != want.why || (got != "" && u.DaysLeft(now) != want.left) {
			t.Errorf("%d: due %q for %q, %d left; want %+v", i, got, u.DueFor, u.DaysLeft(now), want)
		}
		if u.Overdue(now) != (want.due != "" && want.left < 0) {
			t.Errorf("%d: overdue %v", i, u.Overdue(now))
		}
	}
	// Without days for a reason, it has no deadline.
	if us := WithDeadlines([]Update{{EOL: "2026-09-01", EOLPassed: true}}, store.Remediation{Exploited: 7}); !us[0].Due.IsZero() {
		t.Errorf("deadline without days: %v", us[0].Due)
	}
}
