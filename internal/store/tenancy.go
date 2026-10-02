// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Organization is a tenant. Self-hosted installations have exactly one.
type Organization struct {
	ID        string
	Name      string
	Plan      string
	CreatedAt time.Time
}

// Workspace is the data isolation boundary inside an organization, e.g. one MSP client.
type Workspace struct {
	ID        string
	OrgID     string
	Name      string
	Slug      string
	CreatedAt time.Time
}

// Scope identifies the workspace a query runs in. Every workspace-level query takes one.
type Scope struct {
	OrgID       string
	WorkspaceID string
}

// Scope returns the scope of w.
func (w Workspace) Scope() Scope { return Scope{OrgID: w.OrgID, WorkspaceID: w.ID} }

// EnsureDefaultWorkspace returns the oldest workspace of the oldest organization,
// creating an organization and a workspace named "default" when the database is empty.
// Self-hosted installations use it on startup.
func (s *Store) EnsureDefaultWorkspace(ctx context.Context) (Workspace, error) {
	var ws Workspace
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		ws, err = s.firstWorkspace(ctx, tx)
		if !errors.Is(err, ErrNotFound) {
			return err
		}

		now := s.now()
		org := Organization{ID: NewID(), Name: "default", Plan: "oss", CreatedAt: now}
		if _, err := s.exec(ctx, tx,
			`INSERT INTO organizations (id, name, plan, created_at) VALUES (?, ?, ?, ?)`,
			org.ID, org.Name, org.Plan, org.CreatedAt); err != nil {
			return err
		}
		ws = Workspace{ID: NewID(), OrgID: org.ID, Name: "Default", Slug: "default", CreatedAt: now}
		_, err = s.exec(ctx, tx,
			`INSERT INTO workspaces (id, org_id, name, slug, created_at) VALUES (?, ?, ?, ?, ?)`,
			ws.ID, ws.OrgID, ws.Name, ws.Slug, ws.CreatedAt)
		return err
	})
	return ws, err
}

func (s *Store) firstWorkspace(ctx context.Context, q queryer) (Workspace, error) {
	var ws Workspace
	err := s.queryRow(ctx, q, `
		SELECT w.id, w.org_id, w.name, w.slug, w.created_at
		FROM workspaces w JOIN organizations o ON o.id = w.org_id
		ORDER BY o.created_at, o.id, w.created_at, w.id
		LIMIT 1`).Scan(&ws.ID, &ws.OrgID, &ws.Name, &ws.Slug, &ws.CreatedAt)
	return ws, notFound(err)
}

// CreateWorkspace adds a workspace to an existing organization.
func (s *Store) CreateWorkspace(ctx context.Context, orgID, name, slug string) (Workspace, error) {
	ws := Workspace{ID: NewID(), OrgID: orgID, Name: name, Slug: slug, CreatedAt: s.now()}
	_, err := s.exec(ctx, s.db,
		`INSERT INTO workspaces (id, org_id, name, slug, created_at) VALUES (?, ?, ?, ?, ?)`,
		ws.ID, ws.OrgID, ws.Name, ws.Slug, ws.CreatedAt)
	return ws, err
}

// Environment is a stage such as dev, staging or prod. Position orders promotion.
type Environment struct {
	ID        string
	Scope     Scope
	Name      string
	Position  int
	CreatedAt time.Time
}

// CreateEnvironment adds an environment to a workspace.
func (s *Store) CreateEnvironment(ctx context.Context, sc Scope, name string, position int) (Environment, error) {
	env := Environment{ID: NewID(), Scope: sc, Name: name, Position: position, CreatedAt: s.now()}
	_, err := s.exec(ctx, s.db, `
		INSERT INTO environments (id, org_id, workspace_id, name, position, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		env.ID, sc.OrgID, sc.WorkspaceID, env.Name, env.Position, env.CreatedAt)
	return env, err
}

// ListEnvironments returns a workspace's environments in promotion order.
func (s *Store) ListEnvironments(ctx context.Context, sc Scope) ([]Environment, error) {
	rows, err := s.query(ctx, s.db, `
		SELECT id, name, position, created_at FROM environments
		WHERE org_id = ? AND workspace_id = ?
		ORDER BY position, name`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var envs []Environment
	for rows.Next() {
		env := Environment{Scope: sc}
		if err := rows.Scan(&env.ID, &env.Name, &env.Position, &env.CreatedAt); err != nil {
			return nil, err
		}
		env.CreatedAt = env.CreatedAt.UTC()
		envs = append(envs, env)
	}
	return envs, rows.Err()
}
