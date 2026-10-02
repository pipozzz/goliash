// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"strings"
)

// OrgWide reports whether an organization role reaches every workspace.
func OrgWide(role string) bool { return role == RoleOwner || role == RoleAdmin }

// Access is a workspace a user may open and the role they have there.
type Access struct {
	Workspace Workspace
	Role      string
}

// UserWorkspaces lists the workspaces a user may open, in creation order: all of the
// organization's for owners and admins, the ones they are members of for everyone else.
func (s *Store) UserWorkspaces(ctx context.Context, u User) ([]Access, error) {
	if OrgWide(u.Role) {
		all, err := s.ListOrgWorkspaces(ctx, u.OrgID)
		if err != nil {
			return nil, err
		}
		out := make([]Access, len(all))
		for i, w := range all {
			out[i] = Access{Workspace: w, Role: u.Role}
		}
		return out, nil
	}
	rows, err := s.query(ctx, s.db, `SELECT w.id, w.org_id, w.name, w.slug, w.created_at, m.role
		FROM memberships m JOIN workspaces w ON w.id = m.workspace_id
		WHERE m.user_id = ? AND w.org_id = ? ORDER BY w.created_at, w.id`, u.ID, u.OrgID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Access
	for rows.Next() {
		var a Access
		if err := rows.Scan(&a.Workspace.ID, &a.Workspace.OrgID, &a.Workspace.Name, &a.Workspace.Slug, &a.Workspace.CreatedAt, &a.Role); err != nil {
			return nil, err
		}
		a.Workspace.CreatedAt = a.Workspace.CreatedAt.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetMembership gives a user a role in a workspace, or changes it.
func (s *Store) SetMembership(ctx context.Context, userID, workspaceID, role string) error {
	_, err := s.exec(ctx, s.db, `INSERT INTO memberships (user_id, workspace_id, role, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, workspace_id) DO UPDATE SET role = excluded.role`, userID, workspaceID, role, s.now())
	return err
}

// RemoveMembership takes a user's access to a workspace away.
func (s *Store) RemoveMembership(ctx context.Context, userID, workspaceID string) error {
	_, err := s.exec(ctx, s.db, `DELETE FROM memberships WHERE user_id = ? AND workspace_id = ?`, userID, workspaceID)
	return err
}

// WorkspaceRoles returns user ID -> role for the members of a workspace.
func (s *Store) WorkspaceRoles(ctx context.Context, workspaceID string) (map[string]string, error) {
	rows, err := s.query(ctx, s.db, `SELECT user_id, role FROM memberships WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var u, r string
		if err := rows.Scan(&u, &r); err != nil {
			return nil, err
		}
		out[u] = r
	}
	return out, rows.Err()
}

// ListOrgWorkspaces returns an organization's workspaces in creation order.
func (s *Store) ListOrgWorkspaces(ctx context.Context, orgID string) ([]Workspace, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, org_id, name, slug, created_at FROM workspaces
		WHERE org_id = ? ORDER BY created_at, id`, orgID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Workspace
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.OrgID, &w.Name, &w.Slug, &w.CreatedAt); err != nil {
			return nil, err
		}
		w.CreatedAt = w.CreatedAt.UTC()
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetWorkspaceBySlug returns an organization's workspace.
func (s *Store) GetWorkspaceBySlug(ctx context.Context, orgID, slug string) (Workspace, error) {
	var w Workspace
	err := s.queryRow(ctx, s.db, `SELECT id, org_id, name, slug, created_at FROM workspaces WHERE org_id = ? AND slug = ?`,
		orgID, strings.ToLower(slug)).Scan(&w.ID, &w.OrgID, &w.Name, &w.Slug, &w.CreatedAt)
	w.CreatedAt = w.CreatedAt.UTC()
	return w, notFound(err)
}

// WorkspaceCounts is what a workspace holds, for the workspaces page.
type WorkspaceCounts struct {
	Targets, Services, Members int
}

// CountWorkspace returns how many targets, services and members a workspace has.
func (s *Store) CountWorkspace(ctx context.Context, workspaceID string) (WorkspaceCounts, error) {
	var c WorkspaceCounts
	err := s.queryRow(ctx, s.db, `SELECT
		(SELECT COUNT(*) FROM targets WHERE workspace_id = ?),
		(SELECT COUNT(*) FROM services WHERE workspace_id = ?),
		(SELECT COUNT(*) FROM memberships WHERE workspace_id = ?)`, workspaceID, workspaceID, workspaceID).
		Scan(&c.Targets, &c.Services, &c.Members)
	return c, err
}
