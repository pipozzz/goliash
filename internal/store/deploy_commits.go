// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"time"
)

// DeployCommit is the commit a GitOps application had synced when a service moved to a version in an environment,
// and once looked up, the pull request that merged it.
type DeployCommit struct {
	Scope                      Scope
	ID                         string
	ServiceID, EnvironmentID   string
	To                         string // the version deployed
	Repo, Revision             string
	DeployedAt                 time.Time
	PullURL, PullTitle, PullBy string    // empty when no pull request merged it, or not looked up yet
	PullMergedAt               time.Time // zero without one
	LookedUp                   bool
}

// RecordDeployCommit keeps the commit behind a deploy; the same one again is left as it is.
func (s *Store) RecordDeployCommit(ctx context.Context, d DeployCommit) error {
	_, err := s.exec(ctx, s.db, `INSERT INTO deploy_commits (id, org_id, workspace_id, service_id, environment_id,
		to_version, repo, revision, deployed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, service_id, environment_id, to_version, revision) DO NOTHING`,
		NewID(), d.Scope.OrgID, d.Scope.WorkspaceID, d.ServiceID, d.EnvironmentID, d.To, d.Repo, d.Revision, d.DeployedAt.UTC())
	return err
}

// PendingDeployCommits are the commits of deploys since since whose pull request has not been looked up, in every
// workspace, oldest first.
func (s *Store) PendingDeployCommits(ctx context.Context, since time.Time, limit int) ([]DeployCommit, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, org_id, workspace_id, service_id, environment_id, to_version, repo,
		revision, deployed_at FROM deploy_commits WHERE looked_up_at IS NULL AND deployed_at >= ? ORDER BY deployed_at LIMIT ?`,
		since.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DeployCommit
	for rows.Next() {
		var d DeployCommit
		if err := rows.Scan(&d.ID, &d.Scope.OrgID, &d.Scope.WorkspaceID, &d.ServiceID, &d.EnvironmentID, &d.To, &d.Repo,
			&d.Revision, &d.DeployedAt); err != nil {
			return nil, err
		}
		d.DeployedAt = d.DeployedAt.UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetDeployCommitPull records what the forge said about a deploy's commit: the pull request that merged it, or
// none (an empty url).
func (s *Store) SetDeployCommitPull(ctx context.Context, sc Scope, id, pullURL, title, by string, mergedAt time.Time) error {
	var merged any
	if !mergedAt.IsZero() {
		merged = mergedAt.UTC()
	}
	res, err := s.exec(ctx, s.db, `UPDATE deploy_commits SET pull_url = ?, pull_title = ?, pull_by = ?, pull_merged_at = ?,
		looked_up_at = ? WHERE org_id = ? AND workspace_id = ? AND id = ?`,
		pullURL, title, by, merged, s.now(), sc.OrgID, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// ListDeployCommits returns the workspace's deploy commits since since, oldest first.
func (s *Store) ListDeployCommits(ctx context.Context, sc Scope, since time.Time) ([]DeployCommit, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, service_id, environment_id, to_version, repo, revision, deployed_at,
		pull_url, pull_title, pull_by, pull_merged_at, looked_up_at FROM deploy_commits
		WHERE org_id = ? AND workspace_id = ? AND deployed_at >= ? ORDER BY deployed_at`, sc.OrgID, sc.WorkspaceID, since.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DeployCommit
	for rows.Next() {
		d := DeployCommit{Scope: sc}
		var merged, looked sql.NullTime
		if err := rows.Scan(&d.ID, &d.ServiceID, &d.EnvironmentID, &d.To, &d.Repo, &d.Revision, &d.DeployedAt,
			&d.PullURL, &d.PullTitle, &d.PullBy, &merged, &looked); err != nil {
			return nil, err
		}
		d.DeployedAt = d.DeployedAt.UTC()
		if merged.Valid {
			d.PullMergedAt = merged.Time.UTC()
		}
		d.LookedUp = looked.Valid
		out = append(out, d)
	}
	return out, rows.Err()
}
