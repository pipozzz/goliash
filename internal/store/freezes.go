// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"time"
)

// Freeze is a change freeze: from StartsAt to EndsAt the workspace takes no change it does not have to.
type Freeze struct {
	ID               string
	Scope            Scope
	StartsAt, EndsAt time.Time
	Note             string
	CreatedBy        string
	CreatedAt        time.Time
}

// AddFreeze records a change freeze.
func (s *Store) AddFreeze(ctx context.Context, f Freeze) (Freeze, error) {
	f.ID, f.CreatedAt = NewID(), s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO change_freezes (id, org_id, workspace_id, starts_at, ends_at, note, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, f.ID, f.Scope.OrgID, f.Scope.WorkspaceID, f.StartsAt.UTC(), f.EndsAt.UTC(), f.Note, f.CreatedBy, f.CreatedAt.UTC())
	return f, err
}

// ListFreezes returns the workspace's change freezes that have not ended before since, soonest first.
func (s *Store) ListFreezes(ctx context.Context, sc Scope, since time.Time) ([]Freeze, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, starts_at, ends_at, note, created_by, created_at FROM change_freezes
		WHERE org_id = ? AND workspace_id = ? AND ends_at > ? ORDER BY starts_at`, sc.OrgID, sc.WorkspaceID, since.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Freeze
	for rows.Next() {
		f := Freeze{Scope: sc}
		if err := rows.Scan(&f.ID, &f.StartsAt, &f.EndsAt, &f.Note, &f.CreatedBy, &f.CreatedAt); err != nil {
			return nil, err
		}
		f.StartsAt, f.EndsAt, f.CreatedAt = f.StartsAt.UTC(), f.EndsAt.UTC(), f.CreatedAt.UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

// ActiveFreeze is the change freeze the workspace is in at the moment, the one that ends last when they overlap.
func (s *Store) ActiveFreeze(ctx context.Context, sc Scope, at time.Time) (Freeze, bool, error) {
	list, err := s.ListFreezes(ctx, sc, at)
	if err != nil {
		return Freeze{}, false, err
	}
	var best Freeze
	ok := false
	for _, f := range list {
		if !f.StartsAt.After(at) && f.EndsAt.After(at) && (!ok || f.EndsAt.After(best.EndsAt)) {
			best, ok = f, true
		}
	}
	return best, ok, nil
}

// DeleteFreeze removes a change freeze.
func (s *Store) DeleteFreeze(ctx context.Context, sc Scope, id string) error {
	res, err := s.exec(ctx, s.db, `DELETE FROM change_freezes WHERE org_id = ? AND workspace_id = ? AND id = ?`, sc.OrgID, sc.WorkspaceID, id)
	return expectOne(res, err)
}
