// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"time"
)

// SecurityWatch is software a workspace keeps an eye on after an advance notice: what to look for (php, openssl,
// a service or image), why, and until when (zero: until removed).
type SecurityWatch struct {
	Scope     Scope
	ID        string
	Term      string
	Note      string
	Until     time.Time
	CreatedBy string
	CreatedAt time.Time
}

// Active tells whether the watch still holds at now.
func (w SecurityWatch) Active(now time.Time) bool { return w.Until.IsZero() || now.Before(w.Until) }

// AddWatch records a security watch.
func (s *Store) AddWatch(ctx context.Context, w SecurityWatch) (SecurityWatch, error) {
	w.ID, w.CreatedAt = NewID(), s.now()
	var until any
	if !w.Until.IsZero() {
		until = w.Until.UTC()
	}
	_, err := s.exec(ctx, s.db, `INSERT INTO security_watches (id, org_id, workspace_id, term, note, until_at, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, w.ID, w.Scope.OrgID, w.Scope.WorkspaceID, w.Term, w.Note, until, w.CreatedBy, w.CreatedAt.UTC())
	return w, err
}

// ListWatches returns the workspace's security watches still active at now, newest first.
func (s *Store) ListWatches(ctx context.Context, sc Scope, now time.Time) ([]SecurityWatch, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, term, note, until_at, created_by, created_at FROM security_watches
		WHERE org_id = ? AND workspace_id = ? AND (until_at IS NULL OR until_at > ?) ORDER BY created_at DESC`,
		sc.OrgID, sc.WorkspaceID, now.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SecurityWatch
	for rows.Next() {
		w := SecurityWatch{Scope: sc}
		var until sql.NullTime
		if err := rows.Scan(&w.ID, &w.Term, &w.Note, &until, &w.CreatedBy, &w.CreatedAt); err != nil {
			return nil, err
		}
		if until.Valid {
			w.Until = until.Time.UTC()
		}
		w.CreatedAt = w.CreatedAt.UTC()
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeleteWatch removes a security watch.
func (s *Store) DeleteWatch(ctx context.Context, sc Scope, id string) error {
	res, err := s.exec(ctx, s.db, `DELETE FROM security_watches WHERE org_id = ? AND workspace_id = ? AND id = ?`, sc.OrgID, sc.WorkspaceID, id)
	return expectOne(res, err)
}
