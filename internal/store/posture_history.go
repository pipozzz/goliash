// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"time"
)

// postureKept is how long the posture trend is kept: a year and a bit, for year-on-year audits.
const postureKept = 400 * 24 * time.Hour

// PostureSample is the security posture of production on one day.
type PostureSample struct {
	Day       string // YYYY-MM-DD, UTC
	Running   int
	Exposed   int
	Median    int
	P90       int
	Behind30  int
	Behind90  int
	EOLPassed int
	EOLSoon   int
	Accepted  int
}

// RecordPosture stores today's posture, replacing an earlier reading of the same day.
func (s *Store) RecordPosture(ctx context.Context, sc Scope, p PostureSample) error {
	_, err := s.exec(ctx, s.db, `INSERT INTO posture_history (workspace_id, day, running, exposed, median_days, p90_days,
		behind_30, behind_90, eol_passed, eol_soon, accepted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, day) DO UPDATE SET running = excluded.running, exposed = excluded.exposed,
		median_days = excluded.median_days, p90_days = excluded.p90_days, behind_30 = excluded.behind_30,
		behind_90 = excluded.behind_90, eol_passed = excluded.eol_passed, eol_soon = excluded.eol_soon,
		accepted = excluded.accepted`,
		sc.WorkspaceID, p.Day, p.Running, p.Exposed, p.Median, p.P90, p.Behind30, p.Behind90, p.EOLPassed, p.EOLSoon, p.Accepted)
	return err
}

// PostureHistory returns the posture from the day since on (YYYY-MM-DD), oldest first.
func (s *Store) PostureHistory(ctx context.Context, sc Scope, since string) ([]PostureSample, error) {
	rows, err := s.query(ctx, s.db, `SELECT day, running, exposed, median_days, p90_days, behind_30, behind_90, eol_passed,
		eol_soon, accepted FROM posture_history WHERE workspace_id = ? AND day >= ? ORDER BY day`, sc.WorkspaceID, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PostureSample
	for rows.Next() {
		var p PostureSample
		if err := rows.Scan(&p.Day, &p.Running, &p.Exposed, &p.Median, &p.P90, &p.Behind30, &p.Behind90, &p.EOLPassed,
			&p.EOLSoon, &p.Accepted); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
