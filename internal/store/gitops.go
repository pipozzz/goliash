// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"time"
)

// WorkloadGitOps is the GitOps application that deploys a workload (an Argo CD Application), as the latest
// snapshot of its target reports it.
type WorkloadGitOps struct {
	TargetID       string
	WorkloadID     string
	Tool           string // argocd
	App            string
	AppNamespace   string
	Project        string
	Repo           string
	Path           string    // or the Helm chart
	TargetRevision string    // the branch, tag or chart version it follows
	Revision       string    // the commit or chart version last synced
	Sync           string    // Synced, OutOfSync, Unknown
	Health         string    // Healthy, Progressing, Degraded, Suspended, Missing, Unknown
	SyncSince      time.Time // since when Sync and Health are what they are
	UpdatedAt      time.Time
}

// SetWorkloadGitOps records what a target's snapshot says about the GitOps applications of its workloads. A
// complete snapshot replaces the target's records; an incomplete one only updates the workloads it has. SyncSince
// carries over while sync and health stay the same, so "out of sync for an hour" can be told.
func (s *Store) SetWorkloadGitOps(ctx context.Context, sc Scope, targetID string, rows []WorkloadGitOps, complete bool, at time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		since := map[string]WorkloadGitOps{}
		existing, err := s.query(ctx, tx, `SELECT workload_id, sync, health, sync_since FROM workload_gitops
			WHERE org_id = ? AND workspace_id = ? AND target_id = ?`, sc.OrgID, sc.WorkspaceID, targetID)
		if err != nil {
			return err
		}
		for existing.Next() {
			var g WorkloadGitOps
			if err := existing.Scan(&g.WorkloadID, &g.Sync, &g.Health, &g.SyncSince); err != nil {
				_ = existing.Close()
				return err
			}
			since[g.WorkloadID] = g
		}
		_ = existing.Close()
		if err := existing.Err(); err != nil {
			return err
		}
		if complete {
			if _, err := s.exec(ctx, tx, `DELETE FROM workload_gitops WHERE org_id = ? AND workspace_id = ? AND target_id = ?`,
				sc.OrgID, sc.WorkspaceID, targetID); err != nil {
				return err
			}
		}
		for _, g := range rows {
			g.SyncSince = at
			if old, ok := since[g.WorkloadID]; ok && old.Sync == g.Sync && old.Health == g.Health {
				g.SyncSince = old.SyncSince
			}
			if !complete {
				if _, err := s.exec(ctx, tx, `DELETE FROM workload_gitops WHERE org_id = ? AND workspace_id = ? AND target_id = ? AND workload_id = ?`,
					sc.OrgID, sc.WorkspaceID, targetID, g.WorkloadID); err != nil {
					return err
				}
			}
			if _, err := s.exec(ctx, tx, `INSERT INTO workload_gitops (target_id, workload_id, org_id, workspace_id, tool, app,
				app_namespace, project, repo, path, target_revision, revision, sync, health, sync_since, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				targetID, g.WorkloadID, sc.OrgID, sc.WorkspaceID, g.Tool, g.App, g.AppNamespace, g.Project, g.Repo, g.Path,
				g.TargetRevision, g.Revision, g.Sync, g.Health, g.SyncSince.UTC(), at.UTC()); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListWorkloadGitOps returns every workload's GitOps application in the workspace.
func (s *Store) ListWorkloadGitOps(ctx context.Context, sc Scope) ([]WorkloadGitOps, error) {
	rows, err := s.query(ctx, s.db, `SELECT target_id, workload_id, tool, app, app_namespace, project, repo, path,
		target_revision, revision, sync, health, sync_since, updated_at FROM workload_gitops
		WHERE org_id = ? AND workspace_id = ? ORDER BY app, target_id, workload_id`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []WorkloadGitOps
	for rows.Next() {
		var g WorkloadGitOps
		if err := rows.Scan(&g.TargetID, &g.WorkloadID, &g.Tool, &g.App, &g.AppNamespace, &g.Project, &g.Repo, &g.Path,
			&g.TargetRevision, &g.Revision, &g.Sync, &g.Health, &g.SyncSince, &g.UpdatedAt); err != nil {
			return nil, err
		}
		g.SyncSince, g.UpdatedAt = g.SyncSince.UTC(), g.UpdatedAt.UTC()
		out = append(out, g)
	}
	return out, rows.Err()
}
