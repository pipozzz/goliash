// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Target is one collected system: a Kubernetes cluster, an ECS cluster, a Nomad region, a Swarm cluster, a
// Docker host or a set of Compose files.
type Target struct {
	ID                  string
	Scope               Scope
	EnvironmentID       string
	AgentID             string // empty when the server collects the target itself
	Platform            string
	Name                string
	Settings            json.RawMessage // platform settings as sent to the agent (agentproto.Target)
	PollIntervalSeconds int
	LastSnapshotAt      time.Time
	CollectorStatus     string // ok, degraded or failing, as last reported by the agent; empty before the first report
	CollectorError      string
	CollectorReportedAt time.Time
	CreatedAt           time.Time
}

// CreateTarget adds a target. ID and CreatedAt are set by the store.
func (s *Store) CreateTarget(ctx context.Context, t Target) (Target, error) {
	if !agentproto.Platform(t.Platform).Valid() {
		return Target{}, fmt.Errorf("unknown platform %q", t.Platform)
	}
	t.ID, t.CreatedAt = NewID(), s.now()
	if len(t.Settings) == 0 {
		t.Settings = json.RawMessage(`{}`)
	}
	if t.PollIntervalSeconds == 0 {
		t.PollIntervalSeconds = 300
	}
	_, err := s.exec(ctx, s.db, `
		INSERT INTO targets (id, org_id, workspace_id, environment_id, agent_id, platform, name, settings,
			poll_interval_seconds, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Scope.OrgID, t.Scope.WorkspaceID, t.EnvironmentID, nullString(t.AgentID), t.Platform, t.Name,
		string(t.Settings), t.PollIntervalSeconds, t.CreatedAt)
	return t, err
}

// GetTarget returns a target of the workspace.
func (s *Store) GetTarget(ctx context.Context, sc Scope, id string) (Target, error) {
	t, err := scanTarget(s.queryRow(ctx, s.db, `SELECT `+targetColumns+` FROM targets
		WHERE org_id = ? AND workspace_id = ? AND id = ?`, sc.OrgID, sc.WorkspaceID, id))
	return t, notFound(err)
}

// ListAgentTargets returns the targets an agent collects, by name.
func (s *Store) ListAgentTargets(ctx context.Context, sc Scope, agentID string) ([]Target, error) {
	rows, err := s.query(ctx, s.db, `SELECT `+targetColumns+` FROM targets
		WHERE org_id = ? AND workspace_id = ? AND agent_id = ?
		ORDER BY name`, sc.OrgID, sc.WorkspaceID, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var targets []Target
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

// ListTargets returns all targets of the workspace by name.
func (s *Store) ListTargets(ctx context.Context, sc Scope) ([]Target, error) {
	rows, err := s.query(ctx, s.db, `SELECT `+targetColumns+` FROM targets
		WHERE org_id = ? AND workspace_id = ? ORDER BY name`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var targets []Target
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

const targetColumns = `id, org_id, workspace_id, environment_id, agent_id, platform, name, settings,
	poll_interval_seconds, last_snapshot_at, collector_status, collector_error, collector_reported_at, created_at`

type scanner interface{ Scan(dest ...any) error }

func scanTarget(row scanner) (Target, error) {
	var (
		t        Target
		agentID  sql.NullString
		settings string
		last     sql.NullTime
		reported sql.NullTime
	)
	if err := row.Scan(&t.ID, &t.Scope.OrgID, &t.Scope.WorkspaceID, &t.EnvironmentID, &agentID, &t.Platform,
		&t.Name, &settings, &t.PollIntervalSeconds, &last, &t.CollectorStatus, &t.CollectorError, &reported,
		&t.CreatedAt); err != nil {
		return Target{}, err
	}
	t.AgentID, t.Settings = agentID.String, json.RawMessage(settings)
	t.LastSnapshotAt, t.CollectorReportedAt, t.CreatedAt = timeOrZero(last), timeOrZero(reported), t.CreatedAt.UTC()
	return t, nil
}

// ReportCollectorStatus stores the collector health an agent reported for one of its targets.
func (s *Store) ReportCollectorStatus(ctx context.Context, sc Scope, agentID, targetID, status, lastError string) error {
	res, err := s.exec(ctx, s.db, `
		UPDATE targets SET collector_status = ?, collector_error = ?, collector_reported_at = ?
		WHERE org_id = ? AND workspace_id = ? AND id = ? AND agent_id = ?`,
		status, lastError, s.now(), sc.OrgID, sc.WorkspaceID, targetID, agentID)
	return expectOne(res, err)
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// ListServerTargets returns the targets of every workspace that the server collects
// itself (no agent assigned).
func (s *Store) ListServerTargets(ctx context.Context) ([]Target, error) {
	rows, err := s.query(ctx, s.db, `SELECT `+targetColumns+` FROM targets WHERE agent_id IS NULL ORDER BY workspace_id, name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var targets []Target
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

// ReportServerCollectorStatus stores the health of a collector the server runs.
func (s *Store) ReportServerCollectorStatus(ctx context.Context, sc Scope, targetID, status, lastError string) error {
	res, err := s.exec(ctx, s.db, `
		UPDATE targets SET collector_status = ?, collector_error = ?, collector_reported_at = ?
		WHERE org_id = ? AND workspace_id = ? AND id = ? AND agent_id IS NULL`,
		status, lastError, s.now(), sc.OrgID, sc.WorkspaceID, targetID)
	return expectOne(res, err)
}
