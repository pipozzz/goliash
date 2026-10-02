// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Release is an upstream version of a service, as found in its registry.
type Release struct {
	ID           string
	ServiceID    string
	Version      string
	Digest       string
	DiscoveredAt time.Time
}

// InsertReleases records versions of a service and returns those not known before.
func (s *Store) InsertReleases(ctx context.Context, sc Scope, serviceID string, versions []string) ([]string, error) {
	var added []string
	now := s.now()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, v := range versions {
			res, err := s.exec(ctx, tx, `INSERT INTO releases (id, org_id, workspace_id, service_id, version, discovered_at)
				VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (service_id, version) DO NOTHING`,
				NewID(), sc.OrgID, sc.WorkspaceID, serviceID, v, now)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added = append(added, v)
			}
		}
		return nil
	})
	return added, err
}

// ListReleases returns the known versions of a service in discovery order.
func (s *Store) ListReleases(ctx context.Context, sc Scope, serviceID string) ([]Release, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, service_id, version, digest, discovered_at FROM releases
		WHERE org_id = ? AND workspace_id = ? AND service_id = ? ORDER BY discovered_at, version`,
		sc.OrgID, sc.WorkspaceID, serviceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Release
	for rows.Next() {
		var r Release
		if err := rows.Scan(&r.ID, &r.ServiceID, &r.Version, &r.Digest, &r.DiscoveredAt); err != nil {
			return nil, err
		}
		r.DiscoveredAt = r.DiscoveredAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordUpstreamCheck stores when a service's upstream was last checked and the error, if any.
func (s *Store) RecordUpstreamCheck(ctx context.Context, sc Scope, serviceID, checkErr string) error {
	_, err := s.exec(ctx, s.db, `UPDATE services SET upstream_checked_at = ?, upstream_error = ?
		WHERE org_id = ? AND workspace_id = ? AND id = ?`, s.now(), checkErr, sc.OrgID, sc.WorkspaceID, serviceID)
	return err
}

// UpstreamStatus returns when a service's upstream was last checked and the error, if any.
func (s *Store) UpstreamStatus(ctx context.Context, sc Scope, serviceID string) (time.Time, string, error) {
	var at sql.NullTime
	var msg string
	err := s.queryRow(ctx, s.db, `SELECT upstream_checked_at, upstream_error FROM services
		WHERE org_id = ? AND workspace_id = ? AND id = ?`, sc.OrgID, sc.WorkspaceID, serviceID).Scan(&at, &msg)
	return timeOrZero(at), msg, notFound(err)
}

// Drift is a difference that should not last: an environment behind the previous
// one, a version behind upstream, or targets of one environment disagreeing.
type Drift struct {
	ID            string
	Scope         Scope
	ServiceID     string
	EnvironmentID string
	Kind          string // env, upstream or inconsistent
	Detail        json.RawMessage
	Since         time.Time
	ResolvedAt    time.Time
}

// OpenDrifts returns the workspace's unresolved drifts.
func (s *Store) OpenDrifts(ctx context.Context, sc Scope) ([]Drift, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, service_id, environment_id, kind, detail, since FROM drifts
		WHERE org_id = ? AND workspace_id = ? AND resolved_at IS NULL ORDER BY since, id`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Drift
	for rows.Next() {
		d := Drift{Scope: sc}
		var detail string
		if err := rows.Scan(&d.ID, &d.ServiceID, &d.EnvironmentID, &d.Kind, &detail, &d.Since); err != nil {
			return nil, err
		}
		d.Detail, d.Since = json.RawMessage(detail), d.Since.UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// OpenDrift records a new drift; Since defaults to now.
func (s *Store) OpenDrift(ctx context.Context, d Drift) (Drift, error) {
	d.ID = NewID()
	if d.Since.IsZero() {
		d.Since = s.now()
	}
	if len(d.Detail) == 0 {
		d.Detail = json.RawMessage(`{}`)
	}
	_, err := s.exec(ctx, s.db, `INSERT INTO drifts (id, org_id, workspace_id, service_id, environment_id, kind, detail, since)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, d.ID, d.Scope.OrgID, d.Scope.WorkspaceID, d.ServiceID, d.EnvironmentID, d.Kind,
		string(d.Detail), d.Since.UTC())
	return d, err
}

// UpdateDriftDetail replaces the detail of an open drift (e.g. a newer upstream version).
func (s *Store) UpdateDriftDetail(ctx context.Context, sc Scope, id string, detail json.RawMessage) error {
	_, err := s.exec(ctx, s.db, `UPDATE drifts SET detail = ? WHERE org_id = ? AND workspace_id = ? AND id = ?`,
		string(detail), sc.OrgID, sc.WorkspaceID, id)
	return err
}

// ResolveDrift closes a drift.
func (s *Store) ResolveDrift(ctx context.Context, sc Scope, id string) error {
	_, err := s.exec(ctx, s.db, `UPDATE drifts SET resolved_at = ? WHERE org_id = ? AND workspace_id = ? AND id = ?`,
		s.now(), sc.OrgID, sc.WorkspaceID, id)
	return err
}

// ListWorkspaces returns every workspace, for background jobs that visit them all.
func (s *Store) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, org_id, name, slug, created_at FROM workspaces ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Workspace
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.OrgID, &w.Name, &w.Slug, &w.CreatedAt); err != nil {
			return nil, err
		}
		w.CreatedAt = w.CreatedAt.UTC()
		out = append(out, w)
	}
	return out, rows.Err()
}
