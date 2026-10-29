// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"time"
)

// VulnException says a vulnerability does not affect a service, and why: VEX's "not affected", with a review date.
type VulnException struct {
	ID            string
	Scope         Scope
	CVE           string
	ServiceID     string
	Justification string // fixed, not_present, not_reachable, mitigated_upstream or other
	Note          string
	ReviewAt      time.Time
	CreatedBy     string
	CreatedAt     time.Time
	ClosedAt      time.Time // zero while in force
}

// AddVulnException records that a vulnerability does not affect a service.
func (s *Store) AddVulnException(ctx context.Context, e VulnException) (VulnException, error) {
	e.ID, e.CreatedAt = NewID(), s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO vuln_exceptions (id, org_id, workspace_id, cve, service_id, justification, note,
		review_at, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Scope.OrgID, e.Scope.WorkspaceID, e.CVE, e.ServiceID, e.Justification, e.Note, e.ReviewAt.UTC(), e.CreatedBy, e.CreatedAt)
	return e, err
}

// CloseVulnException withdraws an exception: the vulnerability counts again.
func (s *Store) CloseVulnException(ctx context.Context, sc Scope, id string) error {
	res, err := s.exec(ctx, s.db, `UPDATE vuln_exceptions SET closed_at = ? WHERE workspace_id = ? AND id = ? AND closed_at IS NULL`,
		s.now(), sc.WorkspaceID, id)
	return expectOne(res, err)
}

// ListOpenVulnExceptions returns the exceptions in force, oldest first.
func (s *Store) ListOpenVulnExceptions(ctx context.Context, sc Scope) ([]VulnException, error) {
	return s.listVulnExceptions(ctx, sc, `AND closed_at IS NULL`)
}

// ListVulnExceptionsSince returns the exceptions in force, or withdrawn at or after since.
func (s *Store) ListVulnExceptionsSince(ctx context.Context, sc Scope, since time.Time) ([]VulnException, error) {
	return s.listVulnExceptions(ctx, sc, `AND (closed_at IS NULL OR closed_at >= ?)`, since.UTC())
}

func (s *Store) listVulnExceptions(ctx context.Context, sc Scope, where string, args ...any) ([]VulnException, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, cve, service_id, justification, note, review_at, created_by, created_at, closed_at
		FROM vuln_exceptions WHERE org_id = ? AND workspace_id = ? `+where+` ORDER BY created_at, id`,
		append([]any{sc.OrgID, sc.WorkspaceID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []VulnException
	for rows.Next() {
		e := VulnException{Scope: sc}
		var closed sql.NullTime
		if err := rows.Scan(&e.ID, &e.CVE, &e.ServiceID, &e.Justification, &e.Note, &e.ReviewAt, &e.CreatedBy, &e.CreatedAt, &closed); err != nil {
			return nil, err
		}
		e.ReviewAt, e.CreatedAt, e.ClosedAt = e.ReviewAt.UTC(), e.CreatedAt.UTC(), timeOrZero(closed)
		out = append(out, e)
	}
	return out, rows.Err()
}
