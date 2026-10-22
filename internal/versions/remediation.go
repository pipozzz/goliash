// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// WithDeadlines sets each update's remediation deadline: for each reason it has and the workspace set days for,
// when the reason became known plus those days; the earliest wins. An exploited vulnerability counts from CISA
// listing it while it ran there, a security fix from Goliash finding the release, an end of life from its date.
func WithDeadlines(us []Update, r store.Remediation) []Update {
	for i := range us {
		u := &us[i]
		u.Due, u.DueFor = time.Time{}, ""
		consider := func(days int, since time.Time, why string) {
			if days <= 0 || since.IsZero() {
				return
			}
			due := since.UTC().Truncate(24*time.Hour).AddDate(0, 0, days)
			if u.Due.IsZero() || due.Before(u.Due) {
				u.Due, u.DueFor = due, why
			}
		}
		if len(u.Exploited) > 0 {
			consider(r.Exploited, u.ExploitedSince, "exploited vulnerability")
		}
		if u.SecurityFix != "" {
			consider(r.Security, u.SecuritySince, "security fix")
		}
		if u.EOLPassed {
			if eol, err := time.Parse(time.DateOnly, u.EOL); err == nil {
				consider(r.EOL, eol, "end of life")
			}
		}
	}
	return us
}

// DaysLeft is how many days remain until the deadline: negative once it has passed, 0 on its day.
func (u Update) DaysLeft(now time.Time) int {
	today := now.UTC().Truncate(24 * time.Hour)
	return int(u.Due.Sub(today).Hours() / 24)
}

// Overdue reports whether the update's deadline has passed.
func (u Update) Overdue(now time.Time) bool { return !u.Due.IsZero() && u.DaysLeft(now) < 0 }

// securitySince is when Goliash found the first release after running, up to target, that fixes vulnerabilities.
func securitySince(o Overview, svc, running, target string) time.Time {
	from, ok1 := ParseVersion(running)
	to, ok2 := ParseVersion(target)
	if !ok1 || !ok2 {
		return time.Time{}
	}
	var first time.Time
	for key, at := range o.ReleaseSecurityAt {
		id, ver, _ := strings.Cut(key, "|")
		if id != svc {
			continue
		}
		v, ok := ParseVersion(ver)
		if !ok || v.Compare(from) <= 0 || v.Compare(to) > 0 {
			continue
		}
		if first.IsZero() || at.Before(first) {
			first = at
		}
	}
	return first
}
