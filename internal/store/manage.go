// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// DeleteChannel removes a notification channel with its rules and queued messages.
func (s *Store) DeleteChannel(ctx context.Context, sc Scope, id string) error {
	res, err := s.exec(ctx, s.db, `DELETE FROM notification_channels WHERE workspace_id = ? AND id = ?`, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// DeleteRule removes a notification rule and its queued messages.
func (s *Store) DeleteRule(ctx context.Context, sc Scope, id string) error {
	res, err := s.exec(ctx, s.db, `DELETE FROM notification_rules WHERE workspace_id = ? AND id = ?`, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// SetRulePaused pauses or resumes a notification rule.
func (s *Store) SetRulePaused(ctx context.Context, sc Scope, id string, paused bool) error {
	res, err := s.exec(ctx, s.db, `UPDATE notification_rules SET paused = ? WHERE workspace_id = ? AND id = ?`, paused, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// UpdateEnvironment renames an environment and changes its promotion order.
func (s *Store) UpdateEnvironment(ctx context.Context, sc Scope, id, name string, position int) error {
	res, err := s.exec(ctx, s.db, `UPDATE environments SET name = ?, position = ? WHERE workspace_id = ? AND id = ?`,
		name, position, sc.WorkspaceID, id)
	if err != nil && isUniqueViolation(err) {
		return ErrExists
	}
	return expectOne(res, err)
}

// DeleteEnvironment removes an environment. It refuses (ErrInUse) while targets
// belong to it: move or delete them first.
func (s *Store) DeleteEnvironment(ctx context.Context, sc Scope, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := s.queryRow(ctx, tx, `SELECT COUNT(*) FROM targets WHERE workspace_id = ? AND environment_id = ?`,
			sc.WorkspaceID, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrInUse
		}
		res, err := s.exec(ctx, tx, `DELETE FROM environments WHERE workspace_id = ? AND id = ?`, sc.WorkspaceID, id)
		return expectOne(res, err)
	})
}

// DeleteMappingRule removes a mapping rule. Instances it mapped keep their service
// until the next snapshot maps them again.
func (s *Store) DeleteMappingRule(ctx context.Context, sc Scope, id string) error {
	res, err := s.exec(ctx, s.db, `DELETE FROM mapping_rules WHERE workspace_id = ? AND id = ?`, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// DeleteAck removes an acknowledgement, so its notifications come again.
func (s *Store) DeleteAck(ctx context.Context, sc Scope, id string) error {
	res, err := s.exec(ctx, s.db, `DELETE FROM acks WHERE workspace_id = ? AND id = ?`, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// UpdateTarget changes a target's environment, settings and poll interval. The
// collector picks the change up with its next configuration.
func (s *Store) UpdateTarget(ctx context.Context, sc Scope, id, environmentID string, settings json.RawMessage, pollSeconds int) error {
	res, err := s.exec(ctx, s.db, `UPDATE targets SET environment_id = ?, settings = ?, poll_interval_seconds = ?,
		collector_status = '', collector_error = '' WHERE workspace_id = ? AND id = ?`,
		environmentID, string(settings), pollSeconds, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// UpdateChannel renames a notification channel and replaces its configuration,
// sealed like a new one.
func (s *Store) UpdateChannel(ctx context.Context, sc Scope, id, name string, config json.RawMessage) error {
	res, err := s.exec(ctx, s.db, `UPDATE notification_channels SET name = ?, config = ? WHERE workspace_id = ? AND id = ?`,
		name, s.seal(id, string(config)), sc.WorkspaceID, id)
	if err != nil && isUniqueViolation(err) {
		return ErrExists
	}
	return expectOne(res, err)
}

// DeleteService removes a service that no longer runs anywhere, with its releases,
// policy, mapping rules, acknowledgements and drift. History events stay, without
// the service. It refuses (ErrInUse) while instances still run as the service.
func (s *Store) DeleteService(ctx context.Context, sc Scope, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := s.queryRow(ctx, tx, `SELECT COUNT(*) FROM instances WHERE workspace_id = ? AND service_id = ? AND removed_at IS NULL`,
			sc.WorkspaceID, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrInUse
		}
		res, err := s.exec(ctx, tx, `DELETE FROM services WHERE workspace_id = ? AND id = ?`, sc.WorkspaceID, id)
		return expectOne(res, err)
	})
}

// RenameWorkspace changes a workspace's display name; its slug stays.
func (s *Store) RenameWorkspace(ctx context.Context, orgID, id, name string) error {
	res, err := s.exec(ctx, s.db, `UPDATE workspaces SET name = ? WHERE org_id = ? AND id = ?`, name, orgID, id)
	return expectOne(res, err)
}

// WorkspaceAppLabel is the label key a workspace reads applications from first; empty
// for the standard ones only.
func (s *Store) WorkspaceAppLabel(ctx context.Context, workspaceID string) (string, error) {
	var key string
	err := s.queryRow(ctx, s.db, `SELECT app_label FROM workspaces WHERE id = ?`, workspaceID).Scan(&key)
	return key, notFound(err)
}

// SetWorkspaceAppLabel sets that label key.
func (s *Store) SetWorkspaceAppLabel(ctx context.Context, orgID, workspaceID, key string) error {
	res, err := s.exec(ctx, s.db, `UPDATE workspaces SET app_label = ? WHERE org_id = ? AND id = ?`, key, orgID, workspaceID)
	return expectOne(res, err)
}

// RenameService gives a service a new name. It refuses (ErrExists) when another
// service has it: MergeService joins them.
func (s *Store) RenameService(ctx context.Context, sc Scope, id, name string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := s.queryRow(ctx, tx, `SELECT COUNT(*) FROM services WHERE workspace_id = ? AND name = ? AND id <> ?`,
			sc.WorkspaceID, name, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrExists
		}
		res, err := s.exec(ctx, tx, `UPDATE services SET name = ? WHERE org_id = ? AND workspace_id = ? AND id = ?`, name, sc.OrgID, sc.WorkspaceID, id)
		return expectOne(res, err)
	})
}

// MergeService joins service from into service into: its workloads, history, mapping
// rules, acknowledgements, releases and drift move over, then from is deleted. into
// keeps its own name, owner and policy. Open drift that into already has is dropped;
// the rest moves, keeping when it opened and whether it was announced.
func (s *Store) MergeService(ctx context.Context, sc Scope, from, into string) error {
	if from == into {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`UPDATE instances SET service_id = ? WHERE workspace_id = ? AND service_id = ?`,
			`UPDATE events SET service_id = ? WHERE workspace_id = ? AND service_id = ?`,
			`UPDATE mapping_rules SET service_id = ? WHERE workspace_id = ? AND service_id = ?`,
			`UPDATE acks SET service_id = ? WHERE workspace_id = ? AND service_id = ?`,
		} {
			if _, err := s.exec(ctx, tx, q, into, sc.WorkspaceID, from); err != nil {
				return err
			}
		}
		// Releases: into's own stay; versions only from knew move over.
		if _, err := s.exec(ctx, tx, `UPDATE releases SET service_id = ? WHERE service_id = ?
			AND version NOT IN (SELECT version FROM releases WHERE service_id = ?)`, into, from, into); err != nil {
			return err
		}
		// Open drift into already has wins; the rest, and resolved drift, moves.
		if _, err := s.exec(ctx, tx, `DELETE FROM drifts WHERE service_id = ? AND resolved_at IS NULL AND EXISTS (
			SELECT 1 FROM drifts o WHERE o.service_id = ? AND o.resolved_at IS NULL AND o.app = drifts.app
			AND o.environment_id = drifts.environment_id AND o.kind = drifts.kind)`, from, into); err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx, `UPDATE drifts SET service_id = ? WHERE service_id = ?`, into, from); err != nil {
			return err
		}
		res, err := s.exec(ctx, tx, `DELETE FROM services WHERE org_id = ? AND workspace_id = ? AND id = ?`, sc.OrgID, sc.WorkspaceID, from)
		return expectOne(res, err)
	})
}
