// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// OAuthClient is an app that may ask people for access: registered by itself, or described by its Client ID
// Metadata Document at MetadataURL.
type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	MetadataURL  string
	FetchedAt    time.Time
}

// OAuthCode is an authorization code waiting to be exchanged for tokens.
type OAuthCode struct {
	Hash, ClientID, UserID, WorkspaceID, RedirectURI, Challenge, Scope, Resource string
	ExpiresAt                                                                    time.Time
}

// OAuthGrant is one app reading one workspace as one person.
type OAuthGrant struct {
	ID, UserID, WorkspaceID, ClientID, ClientName, Scope, Resource string
	AccessHash, RefreshHash                                        string
	AccessExpiresAt, RefreshExpiresAt, CreatedAt, LastUsedAt       time.Time
	Workspace                                                      string // its name, for the account page
}

// SaveOAuthClient adds a client or refreshes one fetched from its metadata document.
func (s *Store) SaveOAuthClient(ctx context.Context, c OAuthClient) error {
	uris, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return err
	}
	now := s.now()
	var fetched any
	if c.MetadataURL != "" {
		fetched = now
	}
	_, err = s.exec(ctx, s.db, `INSERT INTO oauth_clients (id, name, redirect_uris, metadata_url, created_at, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO UPDATE SET name = excluded.name,
		redirect_uris = excluded.redirect_uris, fetched_at = excluded.fetched_at`,
		c.ID, c.Name, string(uris), c.MetadataURL, now, fetched)
	return err
}

// GetOAuthClient returns a client by its client_id.
func (s *Store) GetOAuthClient(ctx context.Context, id string) (OAuthClient, error) {
	var c OAuthClient
	var uris string
	var fetched sql.NullTime
	err := s.queryRow(ctx, s.db, `SELECT id, name, redirect_uris, metadata_url, fetched_at FROM oauth_clients WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &uris, &c.MetadataURL, &fetched)
	if err != nil {
		return OAuthClient{}, notFound(err)
	}
	c.FetchedAt = fetched.Time
	return c, json.Unmarshal([]byte(uris), &c.RedirectURIs)
}

// CountOAuthClients counts self-registered clients, to cap how many strangers can add.
func (s *Store) CountOAuthClients(ctx context.Context) (int, error) {
	var n int
	err := s.queryRow(ctx, s.db, `SELECT COUNT(*) FROM oauth_clients WHERE metadata_url = ''`).Scan(&n)
	return n, err
}

// CreateOAuthCode stores an authorization code.
func (s *Store) CreateOAuthCode(ctx context.Context, c OAuthCode) error {
	_, err := s.exec(ctx, s.db, `INSERT INTO oauth_codes (hash, client_id, user_id, workspace_id, redirect_uri, challenge,
		scope, resource, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Hash, c.ClientID, c.UserID, c.WorkspaceID, c.RedirectURI, c.Challenge, c.Scope, c.Resource, c.ExpiresAt.UTC())
	return err
}

// ConsumeOAuthCode takes an authorization code: it works once, and not after it expires.
func (s *Store) ConsumeOAuthCode(ctx context.Context, hash string) (OAuthCode, error) {
	var c OAuthCode
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := s.queryRow(ctx, tx, `SELECT hash, client_id, user_id, workspace_id, redirect_uri, challenge, scope,
			resource, expires_at FROM oauth_codes WHERE hash = ?`, hash).Scan(&c.Hash, &c.ClientID, &c.UserID,
			&c.WorkspaceID, &c.RedirectURI, &c.Challenge, &c.Scope, &c.Resource, &c.ExpiresAt); err != nil {
			return notFound(err)
		}
		res, err := s.exec(ctx, tx, `DELETE FROM oauth_codes WHERE hash = ?`, hash)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound // taken by a concurrent request
		}
		return nil
	})
	if err != nil {
		return OAuthCode{}, err
	}
	if !c.ExpiresAt.After(s.now()) {
		return OAuthCode{}, ErrNotFound
	}
	return c, nil
}

// CreateOAuthGrant stores a grant with its first tokens.
func (s *Store) CreateOAuthGrant(ctx context.Context, g OAuthGrant) (OAuthGrant, error) {
	g.ID, g.CreatedAt = NewID(), s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO oauth_grants (id, user_id, workspace_id, client_id, client_name, scope, resource,
		access_hash, access_expires_at, refresh_hash, refresh_expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.UserID, g.WorkspaceID, g.ClientID, g.ClientName, g.Scope, g.Resource, g.AccessHash,
		g.AccessExpiresAt.UTC(), g.RefreshHash, g.RefreshExpiresAt.UTC(), g.CreatedAt)
	return g, err
}

const grantColumns = `g.id, g.user_id, g.workspace_id, g.client_id, g.client_name, g.scope, g.resource, g.access_expires_at,
	g.refresh_expires_at, g.created_at, g.last_used_at, w.name`

func scanGrant(row interface{ Scan(...any) error }) (OAuthGrant, error) {
	var g OAuthGrant
	var used sql.NullTime
	err := row.Scan(&g.ID, &g.UserID, &g.WorkspaceID, &g.ClientID, &g.ClientName, &g.Scope, &g.Resource,
		&g.AccessExpiresAt, &g.RefreshExpiresAt, &g.CreatedAt, &used, &g.Workspace)
	g.LastUsedAt = used.Time
	return g, notFound(err)
}

// OAuthAccess returns the grant of a current access token, and notes that it was used.
func (s *Store) OAuthAccess(ctx context.Context, accessHash string) (OAuthGrant, error) {
	now := s.now()
	g, err := scanGrant(s.queryRow(ctx, s.db, `SELECT `+grantColumns+` FROM oauth_grants g
		JOIN workspaces w ON w.id = g.workspace_id WHERE g.access_hash = ? AND g.access_expires_at > ?`, accessHash, now))
	if err != nil {
		return OAuthGrant{}, err
	}
	_, err = s.exec(ctx, s.db, `UPDATE oauth_grants SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		now, g.ID, now.Add(-time.Minute))
	return g, err
}

// RotateOAuthGrant swaps a refresh token for new tokens. The old refresh token stops working: it is single use,
// and only for the client it was issued to.
func (s *Store) RotateOAuthGrant(ctx context.Context, refreshHash, clientID string, next OAuthGrant) (OAuthGrant, error) {
	now := s.now()
	res, err := s.exec(ctx, s.db, `UPDATE oauth_grants SET access_hash = ?, access_expires_at = ?, refresh_hash = ?,
		refresh_expires_at = ?, last_used_at = ? WHERE refresh_hash = ? AND client_id = ? AND refresh_expires_at > ?`,
		next.AccessHash, next.AccessExpiresAt.UTC(), next.RefreshHash, next.RefreshExpiresAt.UTC(), now, refreshHash, clientID, now)
	if err != nil {
		return OAuthGrant{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return OAuthGrant{}, ErrNotFound
	}
	return scanGrant(s.queryRow(ctx, s.db, `SELECT `+grantColumns+` FROM oauth_grants g
		JOIN workspaces w ON w.id = g.workspace_id WHERE g.refresh_hash = ?`, next.RefreshHash))
}

// ListOAuthGrants returns the apps a person let read Goliash, newest first.
func (s *Store) ListOAuthGrants(ctx context.Context, userID string) ([]OAuthGrant, error) {
	rows, err := s.query(ctx, s.db, `SELECT `+grantColumns+` FROM oauth_grants g JOIN workspaces w ON w.id = g.workspace_id
		WHERE g.user_id = ? AND g.refresh_expires_at > ? ORDER BY g.created_at DESC`, userID, s.now())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OAuthGrant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RevokeOAuthGrant ends a person's grant.
func (s *Store) RevokeOAuthGrant(ctx context.Context, userID, id string) error {
	res, err := s.exec(ctx, s.db, `DELETE FROM oauth_grants WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeOAuthToken ends the grant of an access or refresh token, for the client it belongs to (RFC 7009).
func (s *Store) RevokeOAuthToken(ctx context.Context, hash, clientID string) error {
	_, err := s.exec(ctx, s.db, `DELETE FROM oauth_grants WHERE (access_hash = ? OR refresh_hash = ?) AND client_id = ?`,
		hash, hash, clientID)
	return err
}
