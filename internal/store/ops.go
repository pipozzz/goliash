// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"os"
)

// QueueStats counts a workspace's work in flight, for monitoring the server.
type QueueStats struct {
	PendingSnapshots     int // received, not processed yet
	QueuedNotifications  int // due or waiting for a digest, not sent yet
	FailingNotifications int // not sent and failed at least once
}

// Queues returns a workspace's QueueStats.
func (s *Store) Queues(ctx context.Context, sc Scope) (QueueStats, error) {
	var q QueueStats
	if err := s.queryRow(ctx, s.db, `SELECT COUNT(*) FROM snapshots WHERE workspace_id = ? AND processed_at IS NULL`,
		sc.WorkspaceID).Scan(&q.PendingSnapshots); err != nil {
		return q, err
	}
	err := s.queryRow(ctx, s.db, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN attempts > 0 THEN 1 ELSE 0 END), 0)
		FROM notification_queue WHERE workspace_id = ? AND sent_at IS NULL`, sc.WorkspaceID).
		Scan(&q.QueuedNotifications, &q.FailingNotifications)
	return q, err
}

// ErrBackupUnsupported means the database backs up with its own tools (pg_dump).
var ErrBackupUnsupported = errors.New("PostgreSQL is backed up with pg_dump")

// Backup writes a consistent copy of a SQLite database to path while the server
// runs (VACUUM INTO). path must not exist yet.
func (s *Store) Backup(ctx context.Context, path string) error {
	if s.dialect != SQLite {
		return ErrBackupUnsupported
	}
	if _, err := os.Stat(path); err == nil {
		return os.ErrExist
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}
