// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"time"
)

// MitigationKinds are what a mitigation can be.
var MitigationKinds = []string{"waf", "config", "isolation", "other"}

// Mitigation is what was done about a vulnerability exploited in the wild that has no fix yet, for a service in an
// environment: a WAF rule, a feature turned off, the service cut off from the network.
type Mitigation struct {
	ID                       string
	Scope                    Scope
	CVE                      string
	ServiceID, EnvironmentID string
	Kind                     string // waf, config, isolation or other
	Measure                  string // what was done, in words
	ReviewAt                 time.Time
	CreatedBy                string
	CreatedAt                time.Time
	ClosedAt                 time.Time // zero while in force
	ClosedReason             string    // "a fix is out", "no longer runs", or who withdrew it
}

// CreateMitigation records a mitigation.
func (s *Store) CreateMitigation(ctx context.Context, m Mitigation) (Mitigation, error) {
	m.ID, m.CreatedAt = NewID(), s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO mitigations (id, org_id, workspace_id, cve, service_id, environment_id, kind, measure,
		review_at, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Scope.OrgID, m.Scope.WorkspaceID, m.CVE, m.ServiceID, m.EnvironmentID, m.Kind, m.Measure, m.ReviewAt.UTC(), m.CreatedBy, m.CreatedAt)
	return m, err
}

// ListOpenMitigations returns the mitigations in force, oldest first.
func (s *Store) ListOpenMitigations(ctx context.Context, sc Scope) ([]Mitigation, error) {
	return s.listMitigations(ctx, sc, `AND closed_at IS NULL`)
}

// ListMitigationsSince returns the mitigations in force or closed since a time, oldest first: a report's month.
func (s *Store) ListMitigationsSince(ctx context.Context, sc Scope, since time.Time) ([]Mitigation, error) {
	return s.listMitigations(ctx, sc, `AND (closed_at IS NULL OR closed_at >= ?)`, since.UTC())
}

// CloseMitigation ends a mitigation, saying why.
func (s *Store) CloseMitigation(ctx context.Context, sc Scope, id, reason string) error {
	res, err := s.exec(ctx, s.db, `UPDATE mitigations SET closed_at = ?, closed_reason = ? WHERE workspace_id = ? AND id = ? AND closed_at IS NULL`,
		s.now(), reason, sc.WorkspaceID, id)
	return expectOne(res, err)
}

func (s *Store) listMitigations(ctx context.Context, sc Scope, where string, args ...any) ([]Mitigation, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, cve, service_id, environment_id, kind, measure, review_at, created_by, created_at,
		closed_at, closed_reason FROM mitigations WHERE org_id = ? AND workspace_id = ? `+where+` ORDER BY created_at, id`,
		append([]any{sc.OrgID, sc.WorkspaceID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Mitigation
	for rows.Next() {
		m := Mitigation{Scope: sc}
		var closed sql.NullTime
		if err := rows.Scan(&m.ID, &m.CVE, &m.ServiceID, &m.EnvironmentID, &m.Kind, &m.Measure, &m.ReviewAt, &m.CreatedBy, &m.CreatedAt,
			&closed, &m.ClosedReason); err != nil {
			return nil, err
		}
		m.ReviewAt, m.CreatedAt, m.ClosedAt = m.ReviewAt.UTC(), m.CreatedAt.UTC(), timeOrZero(closed)
		out = append(out, m)
	}
	return out, rows.Err()
}
