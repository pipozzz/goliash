// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SavedView is a named matrix or tiles address with its filters (a team, an environment, TV mode): a person's own,
// or the workspace's when UserID is empty.
type SavedView struct {
	ID        string
	Scope     Scope
	UserID    string // "" for a view everyone in the workspace sees
	Name, URL string
	CreatedAt time.Time
}

// ListSavedViews returns the workspace's shared views, then the person's own, each oldest first.
func (s *Store) ListSavedViews(ctx context.Context, sc Scope, userID string) ([]SavedView, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, user_id, name, url, created_at FROM saved_views
		WHERE org_id = ? AND workspace_id = ? AND (user_id IS NULL OR user_id = ?)
		ORDER BY CASE WHEN user_id IS NULL THEN 0 ELSE 1 END, created_at, name`, sc.OrgID, sc.WorkspaceID, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SavedView
	for rows.Next() {
		v := SavedView{Scope: sc}
		var user sql.NullString
		if err := rows.Scan(&v.ID, &user, &v.Name, &v.URL, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.UserID, v.CreatedAt = user.String, v.CreatedAt.UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateSavedView stores a view.
func (s *Store) CreateSavedView(ctx context.Context, v SavedView) (SavedView, error) {
	v.ID, v.CreatedAt = NewID(), s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO saved_views (id, org_id, workspace_id, user_id, name, url, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.Scope.OrgID, v.Scope.WorkspaceID, nullString(v.UserID), v.Name, v.URL, v.CreatedAt)
	return v, err
}

// GetSavedView returns one of the workspace's views.
func (s *Store) GetSavedView(ctx context.Context, sc Scope, id string) (SavedView, error) {
	v := SavedView{Scope: sc}
	var user sql.NullString
	err := s.queryRow(ctx, s.db, `SELECT id, user_id, name, url, created_at FROM saved_views WHERE org_id = ? AND workspace_id = ? AND id = ?`,
		sc.OrgID, sc.WorkspaceID, id).Scan(&v.ID, &user, &v.Name, &v.URL, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	v.UserID = user.String
	return v, err
}

// DeleteSavedView removes a view.
func (s *Store) DeleteSavedView(ctx context.Context, sc Scope, id string) error {
	_, err := s.exec(ctx, s.db, `DELETE FROM saved_views WHERE org_id = ? AND workspace_id = ? AND id = ?`, sc.OrgID, sc.WorkspaceID, id)
	return err
}
