// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"time"
)

// GitOpsPull is a pull (or merge) request Goliash opened to move a workload to a version.
type GitOpsPull struct {
	Scope                Scope
	ID                   string
	ServiceID            string
	Environment          string
	TargetID, WorkloadID string
	From, To             string
	URL                  string
	OpenedBy             string // who asked, or "auto-pr" for the version policy
	OpenedAt             time.Time
}

// RecordGitOpsPull keeps a pull request Goliash opened; the same workload and version again keeps the first.
func (s *Store) RecordGitOpsPull(ctx context.Context, p GitOpsPull) error {
	p.ID, p.OpenedAt = NewID(), s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO gitops_pulls (id, org_id, workspace_id, service_id, environment, target_id,
		workload_id, from_version, to_version, url, opened_by, opened_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, target_id, workload_id, to_version) DO NOTHING`,
		p.ID, p.Scope.OrgID, p.Scope.WorkspaceID, p.ServiceID, p.Environment, p.TargetID, p.WorkloadID, p.From, p.To, p.URL,
		p.OpenedBy, p.OpenedAt)
	return err
}

// ListGitOpsPulls returns the pull requests Goliash opened in the workspace, newest first.
func (s *Store) ListGitOpsPulls(ctx context.Context, sc Scope) ([]GitOpsPull, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, service_id, environment, target_id, workload_id, from_version, to_version,
		url, opened_by, opened_at FROM gitops_pulls WHERE org_id = ? AND workspace_id = ? ORDER BY opened_at DESC`,
		sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []GitOpsPull
	for rows.Next() {
		p := GitOpsPull{Scope: sc}
		if err := rows.Scan(&p.ID, &p.ServiceID, &p.Environment, &p.TargetID, &p.WorkloadID, &p.From, &p.To, &p.URL,
			&p.OpenedBy, &p.OpenedAt); err != nil {
			return nil, err
		}
		p.OpenedAt = p.OpenedAt.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}
