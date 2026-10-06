// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Agent is a goliash-agent installation. It authenticates with exactly one agent token.
type Agent struct {
	ID           string
	Scope        Scope
	Name         string
	Version      string
	Hostname     string
	Platforms    []string
	RegisteredAt time.Time // zero until the agent first calls register
	LastSeenAt   time.Time
	StaleSince   time.Time // set while the agent misses heartbeats
	CreatedAt    time.Time
	ActiveTokens int // tokens that work; 0 means revoked, 2 means a rotation waits for the agent
}

// CreateAgent creates an agent together with its token. tokenHash is the SHA-256
// hash of the token; the token itself is never stored.
func (s *Store) CreateAgent(ctx context.Context, sc Scope, name, tokenHash string) (Agent, error) {
	now := s.now()
	a := Agent{ID: NewID(), Scope: sc, Name: name, Platforms: []string{}, CreatedAt: now}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `
			INSERT INTO agents (id, org_id, workspace_id, name, platforms, created_at)
			VALUES (?, ?, ?, ?, '[]', ?)`,
			a.ID, sc.OrgID, sc.WorkspaceID, a.Name, a.CreatedAt); err != nil {
			return err
		}
		_, err := s.exec(ctx, tx, `
			INSERT INTO tokens (id, org_id, workspace_id, kind, name, hash, agent_id, created_at)
			VALUES (?, ?, ?, 'agent', ?, ?, ?, ?)`,
			NewID(), sc.OrgID, sc.WorkspaceID, name, tokenHash, a.ID, now)
		return err
	})
	return a, err
}

// AgentByTokenHash returns the agent owning a non-revoked token and records the token's use.
func (s *Store) AgentByTokenHash(ctx context.Context, tokenHash string) (Agent, error) {
	var a Agent
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var tokenID string
		var err error
		a, tokenID, err = s.scanAgent(s.queryRow(ctx, tx, `
			SELECT `+agentColumns+`, t.id
			FROM tokens t JOIN agents a ON a.id = t.agent_id
			WHERE t.hash = ? AND t.kind = 'agent' AND t.revoked_at IS NULL`, tokenHash))
		if err != nil {
			return err
		}
		now := s.now()
		if _, err = s.exec(ctx, tx, `UPDATE tokens SET last_used_at = ? WHERE id = ?`, now, tokenID); err != nil {
			return err
		}
		if a.ActiveTokens < 2 {
			return nil
		}
		// A rotated token is in use: the tokens it replaced stop working.
		_, err = s.exec(ctx, tx, `UPDATE tokens SET revoked_at = ? WHERE agent_id = ? AND kind = 'agent' AND revoked_at IS NULL
			AND id <> ? AND created_at < (SELECT created_at FROM tokens WHERE id = ?)`, now, a.ID, tokenID, tokenID)
		a.ActiveTokens = 1
		return err
	})
	return a, err
}

// GetAgent returns an agent of the workspace.
func (s *Store) GetAgent(ctx context.Context, sc Scope, id string) (Agent, error) {
	a, _, err := s.scanAgent(s.queryRow(ctx, s.db, `
		SELECT `+agentColumns+`, ''
		FROM agents a
		WHERE a.org_id = ? AND a.workspace_id = ? AND a.id = ?`, sc.OrgID, sc.WorkspaceID, id))
	return a, err
}

// GetAgentByName returns the workspace's agent with the given name.
func (s *Store) GetAgentByName(ctx context.Context, sc Scope, name string) (Agent, error) {
	a, _, err := s.scanAgent(s.queryRow(ctx, s.db, `
		SELECT `+agentColumns+`, ''
		FROM agents a
		WHERE a.org_id = ? AND a.workspace_id = ? AND a.name = ?`, sc.OrgID, sc.WorkspaceID, name))
	return a, err
}

// RegisterAgent records what an agent reported when it started.
func (s *Store) RegisterAgent(ctx context.Context, sc Scope, id, version, hostname string, platforms []string) error {
	if platforms == nil {
		platforms = []string{}
	}
	p, err := json.Marshal(platforms)
	if err != nil {
		return err
	}
	now := s.now()
	res, err := s.exec(ctx, s.db, `
		UPDATE agents SET version = ?, hostname = ?, platforms = ?, registered_at = ?, last_seen_at = ?
		WHERE org_id = ? AND workspace_id = ? AND id = ?`,
		version, hostname, string(p), now, now, sc.OrgID, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// TouchAgent records that the agent was seen now (heartbeat) and clears its stale mark.
// It reports since when the agent had been marked stale, or zero when it was not.
func (s *Store) TouchAgent(ctx context.Context, sc Scope, id string) (staleSince time.Time, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var since sql.NullTime
		if err := s.queryRow(ctx, tx, `SELECT stale_since FROM agents
			WHERE org_id = ? AND workspace_id = ? AND id = ?`, sc.OrgID, sc.WorkspaceID, id).Scan(&since); err != nil {
			return notFound(err)
		}
		if since.Valid {
			staleSince = since.Time.UTC()
		}
		_, err := s.exec(ctx, tx, `UPDATE agents SET last_seen_at = ?, stale_since = NULL WHERE id = ?`, s.now(), id)
		return err
	})
	return staleSince, err
}

// MarkStaleAgents marks agents last seen before cutoff as stale and returns those
// newly marked. Agents that never connected are left alone.
func (s *Store) MarkStaleAgents(ctx context.Context, cutoff time.Time) ([]Agent, error) {
	var stale []Agent
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := s.query(ctx, tx, `SELECT `+agentColumns+`, '' FROM agents a
			WHERE a.stale_since IS NULL AND a.last_seen_at IS NOT NULL AND a.last_seen_at < ?
			ORDER BY a.id`, cutoff.UTC())
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			a, _, err := s.scanAgent(rows)
			if err != nil {
				return err
			}
			stale = append(stale, a)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		_ = rows.Close()

		now := s.now()
		for i := range stale {
			if _, err := s.exec(ctx, tx, `UPDATE agents SET stale_since = ? WHERE id = ?`, now, stale[i].ID); err != nil {
				return err
			}
			stale[i].StaleSince = now
		}
		return nil
	})
	return stale, err
}

// RevokeAgentTokens revokes every token of an agent.
func (s *Store) RevokeAgentTokens(ctx context.Context, sc Scope, agentID string) error {
	_, err := s.exec(ctx, s.db, `
		UPDATE tokens SET revoked_at = ?
		WHERE org_id = ? AND workspace_id = ? AND agent_id = ? AND revoked_at IS NULL`,
		s.now(), sc.OrgID, sc.WorkspaceID, agentID)
	return err
}

const agentColumns = `a.id, a.org_id, a.workspace_id, a.name, a.version, a.hostname, a.platforms,
	a.registered_at, a.last_seen_at, a.stale_since, a.created_at,
	(SELECT COUNT(*) FROM tokens k WHERE k.agent_id = a.id AND k.kind = 'agent' AND k.revoked_at IS NULL)`

func (s *Store) scanAgent(row scanner) (Agent, string, error) {
	var (
		a                                Agent
		platforms, extra                 string
		registered, lastSeen, staleSince sql.NullTime
	)
	err := row.Scan(&a.ID, &a.Scope.OrgID, &a.Scope.WorkspaceID, &a.Name, &a.Version, &a.Hostname,
		&platforms, &registered, &lastSeen, &staleSince, &a.CreatedAt, &a.ActiveTokens, &extra)
	if err != nil {
		return Agent{}, "", notFound(err)
	}
	if err := json.Unmarshal([]byte(platforms), &a.Platforms); err != nil {
		return Agent{}, "", err
	}
	a.RegisteredAt, a.LastSeenAt, a.StaleSince = timeOrZero(registered), timeOrZero(lastSeen), timeOrZero(staleSince)
	a.CreatedAt = a.CreatedAt.UTC()
	return a, extra, nil
}

func expectOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAgents returns the workspace's agents by name.
func (s *Store) ListAgents(ctx context.Context, sc Scope) ([]Agent, error) {
	rows, err := s.query(ctx, s.db, `SELECT `+agentColumns+`, '' FROM agents a
		WHERE a.org_id = ? AND a.workspace_id = ? ORDER BY a.name`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Agent
	for rows.Next() {
		a, _, err := s.scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
