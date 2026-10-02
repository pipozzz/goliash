// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Snapshot is the full state of one target as sent by an agent. Payload is the
// agentproto.Snapshot JSON as received.
type Snapshot struct {
	ID          string // snapshot_id chosen by the agent
	Scope       Scope
	TargetID    string
	AgentID     string
	CollectedAt time.Time
	ReceivedAt  time.Time
	Complete    bool
	Payload     json.RawMessage
	ProcessedAt time.Time
}

// InsertSnapshot stores a snapshot unless one with the same ID exists, and moves the
// target's last_snapshot_at forward. It reports false for a duplicate.
func (s *Store) InsertSnapshot(ctx context.Context, snap Snapshot) (inserted bool, err error) {
	snap.ReceivedAt = s.now()
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := s.exec(ctx, tx, `
			INSERT INTO snapshots (id, org_id, workspace_id, target_id, agent_id, collected_at, received_at,
				complete, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO NOTHING`,
			snap.ID, snap.Scope.OrgID, snap.Scope.WorkspaceID, snap.TargetID, nullString(snap.AgentID),
			snap.CollectedAt.UTC(), snap.ReceivedAt, snap.Complete, string(snap.Payload))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		inserted = true
		_, err = s.exec(ctx, tx, `
			UPDATE targets SET last_snapshot_at = ?
			WHERE id = ? AND (last_snapshot_at IS NULL OR last_snapshot_at < ?)`,
			snap.CollectedAt.UTC(), snap.TargetID, snap.CollectedAt.UTC())
		return err
	})
	return inserted, err
}

// UnprocessedSnapshots returns up to limit snapshots that have not been diffed yet,
// oldest collected first, across all workspaces.
func (s *Store) UnprocessedSnapshots(ctx context.Context, limit int) ([]Snapshot, error) {
	rows, err := s.query(ctx, s.db, `SELECT `+snapshotColumns+` FROM snapshots
		WHERE processed_at IS NULL
		ORDER BY collected_at, id
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var snaps []Snapshot
	for rows.Next() {
		snap, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snaps = append(snaps, snap)
	}
	return snaps, rows.Err()
}

// PreviousSnapshot returns the latest processed snapshot of the target collected
// before the given one: the base for diffing it.
func (s *Store) PreviousSnapshot(ctx context.Context, snap Snapshot) (Snapshot, error) {
	prev, err := scanSnapshot(s.queryRow(ctx, s.db, `SELECT `+snapshotColumns+` FROM snapshots
		WHERE org_id = ? AND workspace_id = ? AND target_id = ? AND processed_at IS NOT NULL
			AND collected_at < ? AND id <> ?
		ORDER BY collected_at DESC, id DESC
		LIMIT 1`,
		snap.Scope.OrgID, snap.Scope.WorkspaceID, snap.TargetID, snap.CollectedAt.UTC(), snap.ID))
	return prev, notFound(err)
}

// MarkSnapshotProcessed records that the snapshot has been diffed into events.
func (s *Store) MarkSnapshotProcessed(ctx context.Context, sc Scope, id string) error {
	res, err := s.exec(ctx, s.db, `UPDATE snapshots SET processed_at = ?
		WHERE org_id = ? AND workspace_id = ? AND id = ?`, s.now(), sc.OrgID, sc.WorkspaceID, id)
	return expectOne(res, err)
}

const snapshotColumns = `id, org_id, workspace_id, target_id, agent_id, collected_at, received_at, complete,
	payload, processed_at`

func scanSnapshot(row scanner) (Snapshot, error) {
	var (
		snap      Snapshot
		agentID   sql.NullString
		payload   string
		processed sql.NullTime
	)
	if err := row.Scan(&snap.ID, &snap.Scope.OrgID, &snap.Scope.WorkspaceID, &snap.TargetID, &agentID,
		&snap.CollectedAt, &snap.ReceivedAt, &snap.Complete, &payload, &processed); err != nil {
		return Snapshot{}, err
	}
	snap.AgentID, snap.Payload = agentID.String, json.RawMessage(payload)
	snap.CollectedAt, snap.ReceivedAt = snap.CollectedAt.UTC(), snap.ReceivedAt.UTC()
	snap.ProcessedAt = timeOrZero(processed)
	return snap, nil
}
