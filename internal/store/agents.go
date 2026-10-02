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
	CreatedAt    time.Time
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
		_, err = s.exec(ctx, tx, `UPDATE tokens SET last_used_at = ? WHERE id = ?`, s.now(), tokenID)
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

// TouchAgent records that the agent was seen at t (heartbeat).
func (s *Store) TouchAgent(ctx context.Context, sc Scope, id string, t time.Time) error {
	res, err := s.exec(ctx, s.db, `
		UPDATE agents SET last_seen_at = ?
		WHERE org_id = ? AND workspace_id = ? AND id = ?`,
		t.UTC(), sc.OrgID, sc.WorkspaceID, id)
	return expectOne(res, err)
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
	a.registered_at, a.last_seen_at, a.created_at`

func (s *Store) scanAgent(row *sql.Row) (Agent, string, error) {
	var (
		a                    Agent
		platforms, extra     string
		registered, lastSeen sql.NullTime
	)
	err := row.Scan(&a.ID, &a.Scope.OrgID, &a.Scope.WorkspaceID, &a.Name, &a.Version, &a.Hostname,
		&platforms, &registered, &lastSeen, &a.CreatedAt, &extra)
	if err != nil {
		return Agent{}, "", notFound(err)
	}
	if err := json.Unmarshal([]byte(platforms), &a.Platforms); err != nil {
		return Agent{}, "", err
	}
	a.RegisteredAt, a.LastSeenAt, a.CreatedAt = timeOrZero(registered), timeOrZero(lastSeen), a.CreatedAt.UTC()
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
