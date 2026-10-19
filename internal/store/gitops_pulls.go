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
	State                string // open, merged or closed, as the forge last said
}

// RecordGitOpsPull keeps a pull request Goliash opened. The same workload and version again (after one was closed
// without merging) replaces it.
func (s *Store) RecordGitOpsPull(ctx context.Context, p GitOpsPull) error {
	p.ID, p.OpenedAt = NewID(), s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO gitops_pulls (id, org_id, workspace_id, service_id, environment, target_id,
		workload_id, from_version, to_version, url, opened_by, opened_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, target_id, workload_id, to_version) DO UPDATE SET url = excluded.url,
		from_version = excluded.from_version, opened_by = excluded.opened_by, opened_at = excluded.opened_at,
		state = 'open', checked_at = NULL`,
		p.ID, p.Scope.OrgID, p.Scope.WorkspaceID, p.ServiceID, p.Environment, p.TargetID, p.WorkloadID, p.From, p.To, p.URL,
		p.OpenedBy, p.OpenedAt)
	return err
}

// ListGitOpsPulls returns the pull requests Goliash opened in the workspace, newest first.
func (s *Store) ListGitOpsPulls(ctx context.Context, sc Scope) ([]GitOpsPull, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, service_id, environment, target_id, workload_id, from_version, to_version,
		url, opened_by, opened_at, state FROM gitops_pulls WHERE org_id = ? AND workspace_id = ? ORDER BY opened_at DESC`,
		sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []GitOpsPull
	for rows.Next() {
		p := GitOpsPull{Scope: sc}
		if err := rows.Scan(&p.ID, &p.ServiceID, &p.Environment, &p.TargetID, &p.WorkloadID, &p.From, &p.To, &p.URL,
			&p.OpenedBy, &p.OpenedAt, &p.State); err != nil {
			return nil, err
		}
		p.OpenedAt = p.OpenedAt.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetGitOpsPullState records what the forge says about a pull request now.
func (s *Store) SetGitOpsPullState(ctx context.Context, sc Scope, id, state string) error {
	res, err := s.exec(ctx, s.db, `UPDATE gitops_pulls SET state = ?, checked_at = ? WHERE org_id = ? AND workspace_id = ? AND id = ?`,
		state, s.now(), sc.OrgID, sc.WorkspaceID, id)
	return expectOne(res, err)
}
