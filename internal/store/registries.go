// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RegistryCredential lets the server read a private registry for a workspace. Secret is the password
// or token, encrypted at rest; it never leaves the server again.
type RegistryCredential struct {
	ID        string
	Scope     Scope
	Host      string // normalized registry host, e.g. ghcr.io or registry.example.com:5000
	Username  string
	Secret    string
	CreatedBy string
	CreatedAt time.Time
	UsedAt    time.Time // the last time the registry accepted it
	Error     string    // why the registry refused it the last time, empty when it worked
}

// ErrSecretsUnencrypted refuses to store a registry secret while the server has no secret key.
var ErrSecretsUnencrypted = errors.New("the server has no secret key to encrypt registry credentials with (GOLIASH_SECRET_KEY)")

// SetRegistryCredential stores credentials for c.Host, replacing those the workspace had for it.
func (s *Store) SetRegistryCredential(ctx context.Context, c RegistryCredential) (RegistryCredential, error) {
	if s.aead == nil {
		return RegistryCredential{}, ErrSecretsUnencrypted
	}
	sc := c.Scope
	if _, err := s.exec(ctx, s.db, `DELETE FROM registry_credentials WHERE org_id = ? AND workspace_id = ? AND host = ?`,
		sc.OrgID, sc.WorkspaceID, c.Host); err != nil {
		return RegistryCredential{}, err
	}
	c.ID, c.CreatedAt, c.UsedAt, c.Error = NewID(), s.now(), time.Time{}, ""
	_, err := s.exec(ctx, s.db, `INSERT INTO registry_credentials (id, org_id, workspace_id, host, username, secret, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, c.ID, sc.OrgID, sc.WorkspaceID, c.Host, c.Username, s.seal(c.ID, c.Secret), c.CreatedBy, c.CreatedAt)
	return c, err
}

// ListRegistryCredentials returns the workspace's registry credentials by host, secrets opened.
func (s *Store) ListRegistryCredentials(ctx context.Context, sc Scope) ([]RegistryCredential, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, host, username, secret, created_by, created_at, used_at, error
		FROM registry_credentials WHERE org_id = ? AND workspace_id = ? ORDER BY host`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RegistryCredential
	for rows.Next() {
		c := RegistryCredential{Scope: sc}
		var used sql.NullTime
		if err := rows.Scan(&c.ID, &c.Host, &c.Username, &c.Secret, &c.CreatedBy, &c.CreatedAt, &used, &c.Error); err != nil {
			return nil, err
		}
		if c.Secret, err = s.open(c.ID, c.Secret); err != nil {
			return nil, err
		}
		c.CreatedAt, c.UsedAt = c.CreatedAt.UTC(), timeOrZero(used)
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecordRegistryUse notes whether the registry accepted the workspace's credentials for host: when it
// did, or why not.
func (s *Store) RecordRegistryUse(ctx context.Context, sc Scope, host, failure string) error {
	if failure != "" {
		_, err := s.exec(ctx, s.db, `UPDATE registry_credentials SET error = ? WHERE org_id = ? AND workspace_id = ? AND host = ?`,
			failure, sc.OrgID, sc.WorkspaceID, host)
		return err
	}
	_, err := s.exec(ctx, s.db, `UPDATE registry_credentials SET used_at = ?, error = '' WHERE org_id = ? AND workspace_id = ? AND host = ?`,
		s.now(), sc.OrgID, sc.WorkspaceID, host)
	return err
}

// DeleteRegistryCredential removes the workspace's credentials with id and returns their host.
func (s *Store) DeleteRegistryCredential(ctx context.Context, sc Scope, id string) (string, error) {
	var host string
	err := s.queryRow(ctx, s.db, `SELECT host FROM registry_credentials WHERE id = ? AND org_id = ? AND workspace_id = ?`,
		id, sc.OrgID, sc.WorkspaceID).Scan(&host)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	_, err = s.exec(ctx, s.db, `DELETE FROM registry_credentials WHERE id = ?`, id)
	return host, err
}

// checkRegistrySecrets fails when a stored registry secret does not open with the server's key.
func (s *Store) checkRegistrySecrets(ctx context.Context) error {
	var id, secret string
	err := s.queryRow(ctx, s.db, `SELECT id, secret FROM registry_credentials LIMIT 1`).Scan(&id, &secret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	_, err = s.open(id, secret)
	return err
}
