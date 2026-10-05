// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrInUse means something still depends on what was to be deleted.
var ErrInUse = errors.New("in use")

// AgentToken is one token of an agent; only its hash is stored.
type AgentToken struct {
	ID        string
	CreatedAt time.Time
	LastUsed  time.Time // zero when never used
}

// AgentTokens returns an agent's working tokens, oldest first. Two tokens mean a
// rotation: the older one works until the agent first uses the newer one.
func (s *Store) AgentTokens(ctx context.Context, sc Scope, agentID string) ([]AgentToken, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, created_at, last_used_at FROM tokens
		WHERE workspace_id = ? AND agent_id = ? AND kind = 'agent' AND revoked_at IS NULL ORDER BY created_at, id`,
		sc.WorkspaceID, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AgentToken
	for rows.Next() {
		var t AgentToken
		var used sql.NullTime
		if err := rows.Scan(&t.ID, &t.CreatedAt, &used); err != nil {
			return nil, err
		}
		t.CreatedAt, t.LastUsed = t.CreatedAt.UTC(), timeOrZero(used)
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddAgentToken gives an agent a new token (rotation). Older tokens keep working
// until the agent first uses the new one, so the agent can be updated without a gap.
// Tokens that were never used (a pending rotation, or a token of an agent that never
// connected) are revoked: no agent runs with them.
func (s *Store) AddAgentToken(ctx context.Context, sc Scope, agentID, tokenHash string) error {
	if _, err := s.GetAgent(ctx, sc, agentID); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		if _, err := s.exec(ctx, tx, `UPDATE tokens SET revoked_at = ? WHERE agent_id = ? AND kind = 'agent'
			AND revoked_at IS NULL AND last_used_at IS NULL`, now, agentID); err != nil {
			return err
		}
		_, err := s.exec(ctx, tx, `INSERT INTO tokens (id, org_id, workspace_id, kind, name, hash, agent_id, created_at)
			SELECT ?, org_id, workspace_id, 'agent', name, ?, id, ? FROM agents WHERE id = ?`, NewID(), tokenHash, now, agentID)
		return err
	})
}

// RenameAgent changes an agent's name; the agent keeps its token.
func (s *Store) RenameAgent(ctx context.Context, sc Scope, agentID, name string) error {
	res, err := s.exec(ctx, s.db, `UPDATE agents SET name = ? WHERE workspace_id = ? AND id = ?`, name, sc.WorkspaceID, agentID)
	if err != nil && isUniqueViolation(err) {
		return ErrExists
	}
	return expectOne(res, err)
}

// DeleteAgent removes an agent and its tokens. It refuses (ErrInUse) while targets
// still belong to the agent: move or delete them first.
func (s *Store) DeleteAgent(ctx context.Context, sc Scope, agentID string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := s.queryRow(ctx, tx, `SELECT COUNT(*) FROM targets WHERE workspace_id = ? AND agent_id = ?`,
			sc.WorkspaceID, agentID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrInUse
		}
		if _, err := s.exec(ctx, tx, `DELETE FROM tokens WHERE workspace_id = ? AND agent_id = ?`, sc.WorkspaceID, agentID); err != nil {
			return err
		}
		res, err := s.exec(ctx, tx, `DELETE FROM agents WHERE workspace_id = ? AND id = ?`, sc.WorkspaceID, agentID)
		return expectOne(res, err)
	})
}

// SetTargetAgent moves a target to another agent of the workspace, or to the server
// when agentID is empty. The agents pick the change up with their next configuration.
func (s *Store) SetTargetAgent(ctx context.Context, sc Scope, targetID, agentID string) error {
	var agent any
	if agentID != "" {
		if _, err := s.GetAgent(ctx, sc, agentID); err != nil {
			return err
		}
		agent = agentID
	}
	res, err := s.exec(ctx, s.db, `UPDATE targets SET agent_id = ?, collector_status = '', collector_error = ''
		WHERE workspace_id = ? AND id = ?`, agent, sc.WorkspaceID, targetID)
	return expectOne(res, err)
}

// DeleteTarget removes a target with its snapshots and running instances. History
// events stay, without the target.
func (s *Store) DeleteTarget(ctx context.Context, sc Scope, targetID string) error {
	res, err := s.exec(ctx, s.db, `DELETE FROM targets WHERE workspace_id = ? AND id = ?`, sc.WorkspaceID, targetID)
	return expectOne(res, err)
}

func isUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate key")
}
