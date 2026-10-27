// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"time"
)

// SCIMGroup is a group an identity provider pushes over SCIM, with its members' user IDs.
type SCIMGroup struct {
	ID, OrgID, DisplayName, ExternalID string
	CreatedAt                          time.Time
	Members                            []string
}

// CreateSCIMGroup adds a group with its members.
func (s *Store) CreateSCIMGroup(ctx context.Context, g SCIMGroup) (SCIMGroup, error) {
	g.ID, g.CreatedAt = NewID(), s.now()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `INSERT INTO scim_groups (id, org_id, display_name, external_id, created_at) VALUES (?, ?, ?, ?, ?)`,
			g.ID, g.OrgID, g.DisplayName, g.ExternalID, g.CreatedAt); err != nil {
			return err
		}
		return s.addMembers(ctx, tx, g.ID, g.Members)
	})
	return g, err
}

// GetSCIMGroup returns an organization's group with its members.
func (s *Store) GetSCIMGroup(ctx context.Context, orgID, id string) (SCIMGroup, error) {
	g := SCIMGroup{}
	if err := s.queryRow(ctx, s.db, `SELECT id, org_id, display_name, external_id, created_at FROM scim_groups WHERE org_id = ? AND id = ?`,
		orgID, id).Scan(&g.ID, &g.OrgID, &g.DisplayName, &g.ExternalID, &g.CreatedAt); err != nil {
		return g, notFound(err)
	}
	g.CreatedAt = g.CreatedAt.UTC()
	rows, err := s.query(ctx, s.db, `SELECT user_id FROM scim_group_members WHERE group_id = ? ORDER BY user_id`, id)
	if err != nil {
		return g, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return g, err
		}
		g.Members = append(g.Members, u)
	}
	return g, rows.Err()
}

// ListSCIMGroups returns an organization's groups, without members, by name.
func (s *Store) ListSCIMGroups(ctx context.Context, orgID string) ([]SCIMGroup, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, org_id, display_name, external_id, created_at FROM scim_groups WHERE org_id = ?
		ORDER BY display_name, id`, orgID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SCIMGroup
	for rows.Next() {
		var g SCIMGroup
		if err := rows.Scan(&g.ID, &g.OrgID, &g.DisplayName, &g.ExternalID, &g.CreatedAt); err != nil {
			return nil, err
		}
		g.CreatedAt = g.CreatedAt.UTC()
		out = append(out, g)
	}
	return out, rows.Err()
}

// UpdateSCIMGroup renames a group.
func (s *Store) UpdateSCIMGroup(ctx context.Context, orgID, id, displayName, externalID string) error {
	res, err := s.exec(ctx, s.db, `UPDATE scim_groups SET display_name = ?, external_id = ? WHERE org_id = ? AND id = ?`,
		displayName, externalID, orgID, id)
	return expectOne(res, err)
}

// DeleteSCIMGroup removes a group and its memberships.
func (s *Store) DeleteSCIMGroup(ctx context.Context, orgID, id string) error {
	res, err := s.exec(ctx, s.db, `DELETE FROM scim_groups WHERE org_id = ? AND id = ?`, orgID, id)
	return expectOne(res, err)
}

// AddSCIMGroupMembers adds users to a group; those already in it stay.
func (s *Store) AddSCIMGroupMembers(ctx context.Context, groupID string, userIDs []string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error { return s.addMembers(ctx, tx, groupID, userIDs) })
}

// RemoveSCIMGroupMembers takes users out of a group; nil takes everyone out.
func (s *Store) RemoveSCIMGroupMembers(ctx context.Context, groupID string, userIDs []string) error {
	if userIDs == nil {
		_, err := s.exec(ctx, s.db, `DELETE FROM scim_group_members WHERE group_id = ?`, groupID)
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, u := range userIDs {
			if _, err := s.exec(ctx, tx, `DELETE FROM scim_group_members WHERE group_id = ? AND user_id = ?`, groupID, u); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) addMembers(ctx context.Context, tx *sql.Tx, groupID string, userIDs []string) error {
	for _, u := range userIDs {
		if _, err := s.exec(ctx, tx, `INSERT INTO scim_group_members (group_id, user_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, groupID, u); err != nil {
			return err
		}
	}
	return nil
}

// UserSCIMGroups returns the names of the groups a user is in.
func (s *Store) UserSCIMGroups(ctx context.Context, userID string) ([]SCIMGroup, error) {
	rows, err := s.query(ctx, s.db, `SELECT g.id, g.org_id, g.display_name, g.external_id, g.created_at FROM scim_groups g
		JOIN scim_group_members m ON m.group_id = g.id WHERE m.user_id = ? ORDER BY g.display_name`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SCIMGroup
	for rows.Next() {
		var g SCIMGroup
		if err := rows.Scan(&g.ID, &g.OrgID, &g.DisplayName, &g.ExternalID, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
