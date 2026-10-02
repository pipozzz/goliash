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

// Roles, from most to least privileged.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
)

// RoleRank orders roles: a higher rank may do everything a lower one may.
func RoleRank(role string) int {
	switch role {
	case RoleOwner:
		return 4
	case RoleAdmin:
		return 3
	case RoleMember:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// User is a person who signs in.
type User struct {
	ID          string
	OrgID       string
	Email       string
	Name        string
	Role        string
	CreatedAt   time.Time
	LastLoginAt time.Time
}

// ErrExists means a unique value is already taken.
var ErrExists = errors.New("already exists")

// CreateUser adds a user to an organization. Emails are stored lower-cased.
func (s *Store) CreateUser(ctx context.Context, orgID, email, name, role string) (User, error) {
	u := User{ID: NewID(), OrgID: orgID, Email: strings.ToLower(strings.TrimSpace(email)), Name: name, Role: role, CreatedAt: s.now()}
	res, err := s.exec(ctx, s.db, `INSERT INTO users (id, org_id, email, name, role, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (org_id, email) DO NOTHING`, u.ID, u.OrgID, u.Email, u.Name, u.Role, u.CreatedAt)
	if err != nil {
		return User{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return User{}, ErrExists
	}
	return u, nil
}

// CountUsers returns how many users an organization has.
func (s *Store) CountUsers(ctx context.Context, orgID string) (int, error) {
	var n int
	err := s.queryRow(ctx, s.db, `SELECT COUNT(*) FROM users WHERE org_id = ?`, orgID).Scan(&n)
	return n, err
}

// GetUser returns a user by ID.
func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	return scanUser(s.queryRow(ctx, s.db, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// GetUserByEmail returns an organization's user by e-mail.
func (s *Store) GetUserByEmail(ctx context.Context, orgID, email string) (User, error) {
	return scanUser(s.queryRow(ctx, s.db, `SELECT `+userColumns+` FROM users WHERE org_id = ? AND email = ?`,
		orgID, strings.ToLower(strings.TrimSpace(email))))
}

// ListUsers returns an organization's users by e-mail.
func (s *Store) ListUsers(ctx context.Context, orgID string) ([]User, error) {
	rows, err := s.query(ctx, s.db, `SELECT `+userColumns+` FROM users WHERE org_id = ? ORDER BY email`, orgID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetUserRole changes a user's role.
func (s *Store) SetUserRole(ctx context.Context, orgID, userID, role string) error {
	res, err := s.exec(ctx, s.db, `UPDATE users SET role = ? WHERE org_id = ? AND id = ?`, role, orgID, userID)
	return expectOne(res, err)
}

// DeleteUser removes a user and their sessions.
func (s *Store) DeleteUser(ctx context.Context, orgID, userID string) error {
	res, err := s.exec(ctx, s.db, `DELETE FROM users WHERE org_id = ? AND id = ?`, orgID, userID)
	return expectOne(res, err)
}

const userColumns = `id, org_id, email, name, role, created_at, last_login_at`

func scanUser(row scanner) (User, error) {
	var u User
	var last sql.NullTime
	if err := row.Scan(&u.ID, &u.OrgID, &u.Email, &u.Name, &u.Role, &u.CreatedAt, &last); err != nil {
		return User{}, notFound(err)
	}
	u.CreatedAt, u.LastLoginAt = u.CreatedAt.UTC(), timeOrZero(last)
	return u, nil
}

// CreateSession stores a session under the hash of its cookie value.
func (s *Store) CreateSession(ctx context.Context, idHash, userID string, ttl time.Duration) error {
	now := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
			idHash, userID, now, now.Add(ttl)); err != nil {
			return err
		}
		_, err := s.exec(ctx, tx, `UPDATE users SET last_login_at = ? WHERE id = ?`, now, userID)
		return err
	})
}

// SessionUser returns the user of an unexpired session.
func (s *Store) SessionUser(ctx context.Context, idHash string) (User, error) {
	return scanUser(s.queryRow(ctx, s.db, `SELECT u.id, u.org_id, u.email, u.name, u.role, u.created_at, u.last_login_at
		FROM sessions x JOIN users u ON u.id = x.user_id WHERE x.id = ? AND x.expires_at > ?`, idHash, s.now()))
}

// DeleteSession signs a session out.
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.exec(ctx, s.db, `DELETE FROM sessions WHERE id = ?`, idHash)
	return err
}

// CreateLoginToken stores a single-use sign-in token under its hash.
func (s *Store) CreateLoginToken(ctx context.Context, idHash, userID string, ttl time.Duration) error {
	now := s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO login_tokens (id, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		idHash, userID, now.Add(ttl), now)
	return err
}

// ConsumeLoginToken marks a sign-in token used and returns its user. A token works once.
func (s *Store) ConsumeLoginToken(ctx context.Context, idHash string) (User, error) {
	var u User
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := s.exec(ctx, tx, `UPDATE login_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL AND expires_at > ?`,
			s.now(), idHash, s.now())
		if err := expectOne(res, err); err != nil {
			return err
		}
		var userID string
		if err := s.queryRow(ctx, tx, `SELECT user_id FROM login_tokens WHERE id = ?`, idHash).Scan(&userID); err != nil {
			return err
		}
		u, err = scanUser(s.queryRow(ctx, tx, `SELECT `+userColumns+` FROM users WHERE id = ?`, userID))
		return err
	})
	return u, err
}

// CreateAPIToken stores a workspace API token (glsh_api_…) by its hash.
func (s *Store) CreateAPIToken(ctx context.Context, sc Scope, name, hash string) (string, error) {
	id := NewID()
	_, err := s.exec(ctx, s.db, `INSERT INTO tokens (id, org_id, workspace_id, kind, name, hash, created_at)
		VALUES (?, ?, ?, 'api', ?, ?, ?)`, id, sc.OrgID, sc.WorkspaceID, name, hash, s.now())
	return id, err
}

// APITokenScope returns the workspace of a non-revoked API token and records its use.
func (s *Store) APITokenScope(ctx context.Context, hash string) (Scope, error) {
	var sc Scope
	var id string
	err := s.queryRow(ctx, s.db, `SELECT id, org_id, workspace_id FROM tokens
		WHERE hash = ? AND kind = 'api' AND revoked_at IS NULL`, hash).Scan(&id, &sc.OrgID, &sc.WorkspaceID)
	if err != nil {
		return Scope{}, notFound(err)
	}
	_, err = s.exec(ctx, s.db, `UPDATE tokens SET last_used_at = ? WHERE id = ?`, s.now(), id)
	return sc, err
}
