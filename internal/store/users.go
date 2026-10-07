// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
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
	HasPassword bool
	TOTPEnabled bool // two-factor sign-in is on
	Passkeys    int  // passkeys the user signs in with
	// PasswordChangedAt is zero when the user has never set a password.
	PasswordChangedAt time.Time
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

const userColumns = `id, org_id, email, name, role, created_at, last_login_at, password_hash <> '', password_changed_at,
	totp_enabled_at IS NOT NULL, (SELECT COUNT(*) FROM passkeys k WHERE k.user_id = users.id)`

func scanUser(row scanner) (User, error) {
	var u User
	var last, changed sql.NullTime
	if err := row.Scan(&u.ID, &u.OrgID, &u.Email, &u.Name, &u.Role, &u.CreatedAt, &last, &u.HasPassword, &changed, &u.TOTPEnabled,
		&u.Passkeys); err != nil {
		return User{}, notFound(err)
	}
	u.CreatedAt, u.LastLoginAt, u.PasswordChangedAt = u.CreatedAt.UTC(), timeOrZero(last), timeOrZero(changed)
	return u, nil
}

// SetUserName changes a user's display name.
func (s *Store) SetUserName(ctx context.Context, userID, name string) error {
	res, err := s.exec(ctx, s.db, `UPDATE users SET name = ? WHERE id = ?`, name, userID)
	return expectOne(res, err)
}

// UserPasswordHash returns a user's password hash; empty when they have none.
func (s *Store) UserPasswordHash(ctx context.Context, userID string) (string, error) {
	var h string
	err := s.queryRow(ctx, s.db, `SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&h)
	return h, notFound(err)
}

// SetUserPassword stores a password hash (empty removes the password) and signs out
// every session of the user except keepSession (a session ID hash, or empty for all).
func (s *Store) SetUserPassword(ctx context.Context, userID, hash, keepSession string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var changed any
		if hash != "" {
			changed = s.now()
		}
		res, err := s.exec(ctx, tx, `UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?`, hash, changed, userID)
		if err := expectOne(res, err); err != nil {
			return err
		}
		_, err = s.exec(ctx, tx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepSession)
		return err
	})
}

// Session is a signed-in browser.
type Session struct {
	ID        string // SHA-256 of the cookie value
	Method    string // password, link or oidc
	UserAgent string
	IP        string
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
}

// CreateSession stores a session under the hash of its cookie value.
func (s *Store) CreateSession(ctx context.Context, idHash, userID string, ttl time.Duration, x Session) error {
	now := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `INSERT INTO sessions (id, user_id, created_at, expires_at, last_seen_at, user_agent, ip, method)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, idHash, userID, now, now.Add(ttl), now, x.UserAgent, x.IP, x.Method); err != nil {
			return err
		}
		_, err := s.exec(ctx, tx, `UPDATE users SET last_login_at = ? WHERE id = ?`, now, userID)
		return err
	})
}

// SetSessionIdle makes sessions unused for longer than idle stop working (0: never).
func (s *Store) SetSessionIdle(idle time.Duration) { s.sessionIdle = idle }

// SessionUser returns the user and sign-in method of an unexpired session that was
// used within the idle timeout, and records that the session is in use (at most once
// a minute).
func (s *Store) SessionUser(ctx context.Context, idHash string) (User, string, error) {
	now := s.now()
	idleCutoff := time.Time{}
	if s.sessionIdle > 0 {
		idleCutoff = now.Add(-s.sessionIdle)
	}
	var method string
	row := s.queryRow(ctx, s.db, `SELECT u.id, u.org_id, u.email, u.name, u.role, u.created_at, u.last_login_at,
		u.password_hash <> '', u.password_changed_at, u.totp_enabled_at IS NOT NULL,
		(SELECT COUNT(*) FROM passkeys k WHERE k.user_id = u.id), x.method
		FROM sessions x JOIN users u ON u.id = x.user_id
		WHERE x.id = ? AND x.expires_at > ? AND COALESCE(x.last_seen_at, x.created_at) > ?`, idHash, now, idleCutoff)
	var u User
	var last, changed sql.NullTime
	if err := row.Scan(&u.ID, &u.OrgID, &u.Email, &u.Name, &u.Role, &u.CreatedAt, &last, &u.HasPassword, &changed,
		&u.TOTPEnabled, &u.Passkeys, &method); err != nil {
		return User{}, "", notFound(err)
	}
	u.CreatedAt, u.LastLoginAt, u.PasswordChangedAt = u.CreatedAt.UTC(), timeOrZero(last), timeOrZero(changed)
	_, err := s.exec(ctx, s.db, `UPDATE sessions SET last_seen_at = ? WHERE id = ? AND (last_seen_at IS NULL OR last_seen_at < ?)`,
		now, idHash, now.Add(-time.Minute))
	return u, method, err
}

// RequireTwoFactor reports whether an organization requires two-factor sign-in.
func (s *Store) RequireTwoFactor(ctx context.Context, orgID string) (bool, error) {
	var on bool
	err := s.queryRow(ctx, s.db, `SELECT require_2fa FROM organizations WHERE id = ?`, orgID).Scan(&on)
	return on, notFound(err)
}

// SetRequireTwoFactor turns the organization's two-factor requirement on or off.
func (s *Store) SetRequireTwoFactor(ctx context.Context, orgID string, on bool) error {
	res, err := s.exec(ctx, s.db, `UPDATE organizations SET require_2fa = ? WHERE id = ?`, on, orgID)
	return expectOne(res, err)
}

// ListSessions returns a user's unexpired sessions, most recently used first.
func (s *Store) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, method, user_agent, ip, created_at, last_seen_at, expires_at FROM sessions
		WHERE user_id = ? AND expires_at > ? ORDER BY COALESCE(last_seen_at, created_at) DESC`, userID, s.now())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Session
	for rows.Next() {
		var x Session
		var seen sql.NullTime
		if err := rows.Scan(&x.ID, &x.Method, &x.UserAgent, &x.IP, &x.CreatedAt, &seen, &x.ExpiresAt); err != nil {
			return nil, err
		}
		x.CreatedAt, x.ExpiresAt, x.LastSeen = x.CreatedAt.UTC(), x.ExpiresAt.UTC(), timeOrZero(seen)
		if x.LastSeen.IsZero() {
			x.LastSeen = x.CreatedAt
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// DeleteSession signs a session out.
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.exec(ctx, s.db, `DELETE FROM sessions WHERE id = ?`, idHash)
	return err
}

// DeleteUserSession signs out one of a user's sessions.
func (s *Store) DeleteUserSession(ctx context.Context, userID, idHash string) error {
	res, err := s.exec(ctx, s.db, `DELETE FROM sessions WHERE user_id = ? AND id = ?`, userID, idHash)
	return expectOne(res, err)
}

// DeleteUserSessions signs out every session of a user except keep (empty: all).
func (s *Store) DeleteUserSessions(ctx context.Context, userID, keep string) (int, error) {
	res, err := s.exec(ctx, s.db, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keep)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Purposes of sign-in tokens.
const (
	TokenLink         = "link"          // a sign-in link, e-mailed or printed
	TokenSecondFactor = "second-factor" // first factor passed; waiting for the code
	TokenRecovery     = "recovery"      // GOLIASH_RECOVERY_EMAIL: skips the second factor
)

// CreateLoginToken stores a single-use sign-in token under its hash. method records
// how the first factor was passed, for second-factor tokens.
func (s *Store) CreateLoginToken(ctx context.Context, idHash, userID string, ttl time.Duration, purpose, method string) error {
	now := s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO login_tokens (id, user_id, expires_at, created_at, purpose, method) VALUES (?, ?, ?, ?, ?, ?)`,
		idHash, userID, now.Add(ttl), now, purpose, method)
	return err
}

// PeekLoginToken returns the user and method of an unused, unexpired token of one of
// purposes, without using it up.
func (s *Store) PeekLoginToken(ctx context.Context, idHash string, purposes ...string) (User, string, string, error) {
	var userID, purpose, method string
	err := s.queryRow(ctx, s.db, `SELECT user_id, purpose, method FROM login_tokens WHERE id = ? AND used_at IS NULL AND expires_at > ?`,
		idHash, s.now()).Scan(&userID, &purpose, &method)
	if err != nil {
		return User{}, "", "", notFound(err)
	}
	if !slices.Contains(purposes, purpose) {
		return User{}, "", "", ErrNotFound
	}
	u, err := s.GetUser(ctx, userID)
	return u, purpose, method, err
}

// ConsumeLoginToken marks a sign-in token of one of purposes used and returns its
// user and purpose. A token works once.
func (s *Store) ConsumeLoginToken(ctx context.Context, idHash string, purposes ...string) (User, string, error) {
	var u User
	var purpose string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := s.queryRow(ctx, tx, `SELECT purpose FROM login_tokens WHERE id = ?`, idHash).Scan(&purpose); err != nil {
			return notFound(err)
		}
		if !slices.Contains(purposes, purpose) {
			return ErrNotFound
		}
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
	return u, purpose, err
}

// APIToken is a workspace API token (glsh_api_…); only its hash is stored.
type APIToken struct {
	ID        string
	Name      string
	Role      string // viewer or member
	CreatedBy string
	CreatedAt time.Time
	LastUsed  time.Time // zero when never used
	ExpiresAt time.Time // zero when it does not expire
}

// Expired reports whether the token no longer works because of its expiry.
func (t APIToken) Expired(now time.Time) bool {
	return !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt)
}

// CreateAPIToken stores a workspace API token by its hash. t.ID and t.CreatedAt are set here.
func (s *Store) CreateAPIToken(ctx context.Context, sc Scope, t APIToken, hash string) (APIToken, error) {
	t.ID, t.CreatedAt = NewID(), s.now()
	if t.Role == "" {
		t.Role = RoleViewer
	}
	var expires any
	if !t.ExpiresAt.IsZero() {
		expires = t.ExpiresAt.UTC()
	}
	_, err := s.exec(ctx, s.db, `INSERT INTO tokens (id, org_id, workspace_id, kind, name, hash, created_at, role, expires_at, created_by)
		VALUES (?, ?, ?, 'api', ?, ?, ?, ?, ?, ?)`, t.ID, sc.OrgID, sc.WorkspaceID, t.Name, hash, t.CreatedAt, t.Role, expires, t.CreatedBy)
	return t, err
}

// APITokenAuth returns the workspace and the token of a working (not revoked, not
// expired) API token, and records its use at most once a minute.
func (s *Store) APITokenAuth(ctx context.Context, hash string) (Scope, APIToken, error) {
	var sc Scope
	var t APIToken
	now := s.now()
	err := s.queryRow(ctx, s.db, `SELECT id, org_id, workspace_id, name, role FROM tokens
		WHERE hash = ? AND kind = 'api' AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`, hash, now).
		Scan(&t.ID, &sc.OrgID, &sc.WorkspaceID, &t.Name, &t.Role)
	if err != nil {
		return Scope{}, APIToken{}, notFound(err)
	}
	_, err = s.exec(ctx, s.db, `UPDATE tokens SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		now, t.ID, now.Add(-time.Minute))
	return sc, t, err
}

// ListAPITokens returns a workspace's API tokens that are not revoked, newest first.
func (s *Store) ListAPITokens(ctx context.Context, sc Scope) ([]APIToken, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, name, role, created_by, created_at, last_used_at, expires_at FROM tokens
		WHERE workspace_id = ? AND kind = 'api' AND revoked_at IS NULL ORDER BY created_at DESC, name`, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		var used, expires sql.NullTime
		if err := rows.Scan(&t.ID, &t.Name, &t.Role, &t.CreatedBy, &t.CreatedAt, &used, &expires); err != nil {
			return nil, err
		}
		t.CreatedAt, t.LastUsed, t.ExpiresAt = t.CreatedAt.UTC(), timeOrZero(used), timeOrZero(expires)
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken stops an API token of the workspace from working.
func (s *Store) RevokeAPIToken(ctx context.Context, sc Scope, id string) error {
	res, err := s.exec(ctx, s.db, `UPDATE tokens SET revoked_at = ? WHERE workspace_id = ? AND id = ? AND kind = 'api' AND revoked_at IS NULL`,
		s.now(), sc.WorkspaceID, id)
	return expectOne(res, err)
}

// CreateSetupToken stores a one-time link (by hash) that creates the first owner.
func (s *Store) CreateSetupToken(ctx context.Context, idHash string, ttl time.Duration) error {
	now := s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO setup_tokens (id, expires_at, created_at) VALUES (?, ?, ?)`, idHash, now.Add(ttl), now)
	return err
}

// SetupTokenValid reports whether a setup link still works: unused, unexpired, and
// nobody has an account yet.
func (s *Store) SetupTokenValid(ctx context.Context, orgID, idHash string) (bool, error) {
	var n int
	if err := s.queryRow(ctx, s.db, `SELECT COUNT(*) FROM setup_tokens WHERE id = ? AND used_at IS NULL AND expires_at > ?`,
		idHash, s.now()).Scan(&n); err != nil || n == 0 {
		return false, err
	}
	users, err := s.CountUsers(ctx, orgID)
	return users == 0, err
}

// CreateFirstOwner uses a setup link to create the organization's first person, an
// owner with a password. It fails (ErrNotFound) when the link is used, expired or
// someone has an account already.
func (s *Store) CreateFirstOwner(ctx context.Context, orgID, idHash, email, name, passwordHash string) (User, error) {
	var u User
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		var n int
		if err := s.queryRow(ctx, tx, `SELECT COUNT(*) FROM users WHERE org_id = ?`, orgID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrNotFound
		}
		res, err := s.exec(ctx, tx, `UPDATE setup_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL AND expires_at > ?`, now, idHash, now)
		if err := expectOne(res, err); err != nil {
			return err
		}
		u = User{
			ID: NewID(), OrgID: orgID, Email: strings.ToLower(strings.TrimSpace(email)), Name: name, Role: RoleOwner, CreatedAt: now,
			HasPassword: true, PasswordChangedAt: now,
		}
		_, err = s.exec(ctx, tx, `INSERT INTO users (id, org_id, email, name, role, created_at, password_hash, password_changed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, u.ID, u.OrgID, u.Email, u.Name, u.Role, u.CreatedAt, passwordHash, now)
		return err
	})
	return u, err
}

// SigninFailures counts the failed sign-ins of a key (a hash) since a time; every
// server sees the same count.
func (s *Store) SigninFailures(ctx context.Context, key string, since time.Time) (int, error) {
	var n int
	err := s.queryRow(ctx, s.db, `SELECT COUNT(*) FROM signin_failures WHERE key = ? AND at > ?`, key, since.UTC()).Scan(&n)
	return n, err
}

// AddSigninFailure records a failed sign-in of a key.
func (s *Store) AddSigninFailure(ctx context.Context, key string) error {
	_, err := s.exec(ctx, s.db, `INSERT INTO signin_failures (key, at) VALUES (?, ?)`, key, s.now())
	return err
}

// ClearSigninFailures forgets a key's failures after a successful sign-in.
func (s *Store) ClearSigninFailures(ctx context.Context, key string) error {
	_, err := s.exec(ctx, s.db, `DELETE FROM signin_failures WHERE key = ?`, key)
	return err
}
