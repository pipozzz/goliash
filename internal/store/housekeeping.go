// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"time"
)

// HousekeepingResult counts what Housekeep removed.
type HousekeepingResult struct {
	Snapshots     int64
	Notifications int64
	Sessions      int64
	LoginTokens   int64
	History       int64
}

// historyKept is how long ended instance periods are kept for time travel.
const historyKept = 400 * 24 * time.Hour

// Housekeep removes data nothing reads any more:
//
//   - processed snapshots beyond the newest keepSnapshots per target (history lives in
//     instances and events; a snapshot is only needed until it is processed);
//   - delivered notifications older than 30 days, and ones given up on after 30 days;
//   - expired sessions and sign-in tokens.
//   - instance history periods that ended more than 400 days ago.
//
// Unprocessed snapshots are never removed.
func (s *Store) Housekeep(ctx context.Context, keepSnapshots int) (HousekeepingResult, error) {
	var r HousekeepingResult
	if keepSnapshots < 1 {
		keepSnapshots = 1
	}
	now := s.now()
	monthAgo := now.Add(-30 * 24 * time.Hour)
	steps := []struct {
		n     *int64
		query string
		args  []any
	}{
		{&r.Snapshots, `DELETE FROM snapshots WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY target_id ORDER BY collected_at DESC, id DESC) AS rn
				FROM snapshots WHERE processed_at IS NOT NULL
			) ranked WHERE rn > ?)`, []any{keepSnapshots}},
		{&r.Notifications, `DELETE FROM notification_queue WHERE created_at < ? AND (sent_at IS NOT NULL OR attempts > 0)`, []any{monthAgo}},
		{&r.Sessions, `DELETE FROM sessions WHERE expires_at < ?`, []any{now}},
		{&r.LoginTokens, `DELETE FROM login_tokens WHERE expires_at < ?`, []any{now.Add(-24 * time.Hour)}},
		{&r.LoginTokens, `DELETE FROM signin_failures WHERE at < ?`, []any{now.Add(-24 * time.Hour)}},
		{&r.LoginTokens, `DELETE FROM setup_tokens WHERE expires_at < ?`, []any{now.Add(-24 * time.Hour)}},
		{&r.History, `DELETE FROM instance_history WHERE until < ?`, []any{now.Add(-historyKept)}},
	}
	for _, step := range steps {
		res, err := s.exec(ctx, s.db, step.query, step.args...)
		if err != nil {
			return r, err
		}
		n, _ := res.RowsAffected()
		*step.n += n
	}
	return r, nil
}

// CountSnapshots returns how many snapshots are stored, for tests and metrics.
func (s *Store) CountSnapshots(ctx context.Context) (int, error) {
	var n int
	err := s.queryRow(ctx, s.db, `SELECT COUNT(*) FROM snapshots`).Scan(&n)
	return n, err
}
