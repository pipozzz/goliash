// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"strings"
)

// AppNames maps applications found in labels or namespaces to the names people gave
// them: a rename, or a merge when several map to one.
func (s *Store) AppNames(ctx context.Context, sc Scope) (map[string]string, error) {
	rows, err := s.query(ctx, s.db, `SELECT name, shown_as FROM app_names WHERE org_id = ? AND workspace_id = ?`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, err
		}
		out[from] = to
	}
	return out, rows.Err()
}

// RenameApp shows the application name as to from now on, and every application
// already shown as name too (renaming a merged application renames them all). An
// empty to, or to equal to name, undoes it.
func (s *Store) RenameApp(ctx context.Context, sc Scope, name, to string) error {
	name, to = strings.TrimSpace(name), strings.TrimSpace(to)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		// Names already shown as name follow it.
		if to != "" {
			if _, err := s.exec(ctx, tx, `UPDATE app_names SET shown_as = ? WHERE org_id = ? AND workspace_id = ? AND shown_as = ?`,
				to, sc.OrgID, sc.WorkspaceID, name); err != nil {
				return err
			}
		}
		if _, err := s.exec(ctx, tx, `DELETE FROM app_names WHERE org_id = ? AND workspace_id = ? AND (name = ? OR name = shown_as)`,
			sc.OrgID, sc.WorkspaceID, name); err != nil {
			return err
		}
		if to == "" || to == name {
			return nil
		}
		// The application's team follows it, unless the one it merges into has a team.
		if _, err := s.exec(ctx, tx, `DELETE FROM app_teams WHERE org_id = ? AND workspace_id = ? AND app = ?
			AND EXISTS (SELECT 1 FROM app_teams t WHERE t.workspace_id = app_teams.workspace_id AND t.app = ?)`,
			sc.OrgID, sc.WorkspaceID, name, to); err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx, `UPDATE app_teams SET app = ? WHERE org_id = ? AND workspace_id = ? AND app = ?`,
			to, sc.OrgID, sc.WorkspaceID, name); err != nil {
			return err
		}
		_, err := s.exec(ctx, tx, `INSERT INTO app_names (org_id, workspace_id, name, shown_as, created_at) VALUES (?, ?, ?, ?, ?)`,
			sc.OrgID, sc.WorkspaceID, name, to, s.now())
		return err
	})
}

// SetServiceApp places a service in an application by hand; empty: back to its
// workloads' labels.
func (s *Store) SetServiceApp(ctx context.Context, sc Scope, id, app string) error {
	res, err := s.exec(ctx, s.db, `UPDATE services SET app = ? WHERE org_id = ? AND workspace_id = ? AND id = ?`,
		strings.TrimSpace(app), sc.OrgID, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// SetOwners gives every listed service the owner (a team), as set by source: "" for
// people, "label" or "app" for Goliash; it returns how many changed.
func (s *Store) SetOwners(ctx context.Context, sc Scope, serviceIDs []string, owner, source string) (int, error) {
	n := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range serviceIDs {
			res, err := s.exec(ctx, tx, `UPDATE services SET owner = ?, owner_source = ? WHERE org_id = ? AND workspace_id = ? AND id = ?
				AND (owner <> ? OR owner_source <> ?)`,
				strings.TrimSpace(owner), source, sc.OrgID, sc.WorkspaceID, id, strings.TrimSpace(owner), source)
			if err != nil {
				return err
			}
			c, _ := res.RowsAffected()
			n += int(c)
		}
		return nil
	})
	return n, err
}

// RenameOwner renames a team on every service it owns; it returns how many changed.
func (s *Store) RenameOwner(ctx context.Context, sc Scope, from, to string) (int, error) {
	res, err := s.exec(ctx, s.db, `UPDATE services SET owner = ? WHERE org_id = ? AND workspace_id = ? AND owner = ?`,
		strings.TrimSpace(to), sc.OrgID, sc.WorkspaceID, from)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// SetDriftApp moves an open drift to another application (after a rename or merge),
// keeping when it opened and whether it was announced.
func (s *Store) SetDriftApp(ctx context.Context, sc Scope, id, app string) error {
	res, err := s.exec(ctx, s.db, `UPDATE drifts SET app = ? WHERE org_id = ? AND workspace_id = ? AND id = ? AND resolved_at IS NULL`,
		app, sc.OrgID, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// AppTeams maps applications to their team, for services in them without an owner.
func (s *Store) AppTeams(ctx context.Context, sc Scope) (map[string]string, error) {
	rows, err := s.query(ctx, s.db, `SELECT app, team FROM app_teams WHERE org_id = ? AND workspace_id = ?`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var app, team string
		if err := rows.Scan(&app, &team); err != nil {
			return nil, err
		}
		out[app] = team
	}
	return out, rows.Err()
}

// SetAppTeam remembers an application's team; empty forgets it.
func (s *Store) SetAppTeam(ctx context.Context, sc Scope, app, team string) error {
	app, team = strings.TrimSpace(app), strings.TrimSpace(team)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `DELETE FROM app_teams WHERE org_id = ? AND workspace_id = ? AND app = ?`, sc.OrgID, sc.WorkspaceID, app); err != nil {
			return err
		}
		if team == "" {
			return nil
		}
		_, err := s.exec(ctx, tx, `INSERT INTO app_teams (org_id, workspace_id, app, team, created_at) VALUES (?, ?, ?, ?, ?)`,
			sc.OrgID, sc.WorkspaceID, app, team, s.now())
		return err
	})
}
