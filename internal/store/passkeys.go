// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"time"
)

// Passkey is one WebAuthn credential of a user. Credential is the record the WebAuthn
// library keeps (public key, sign count, flags) as JSON.
type Passkey struct {
	ID           string
	UserID       string
	CredentialID string // base64url
	Credential   []byte
	Name         string
	CreatedAt    time.Time
	LastUsedAt   time.Time
}

const passkeyColumns = `id, user_id, credential_id, credential, name, created_at, last_used_at` //nolint:gosec // column names

func scanPasskey(row scanner) (Passkey, error) {
	var k Passkey
	var cred string
	var used sql.NullTime
	if err := row.Scan(&k.ID, &k.UserID, &k.CredentialID, &cred, &k.Name, &k.CreatedAt, &used); err != nil {
		return k, notFound(err)
	}
	k.Credential, k.CreatedAt, k.LastUsedAt = []byte(cred), k.CreatedAt.UTC(), timeOrZero(used)
	return k, nil
}

// AddPasskey stores a new passkey; ErrExists when the credential is known already.
func (s *Store) AddPasskey(ctx context.Context, userID, credentialID string, credential []byte, name string) (Passkey, error) {
	k := Passkey{ID: NewID(), UserID: userID, CredentialID: credentialID, Credential: credential, Name: name, CreatedAt: s.now()}
	_, err := s.exec(ctx, s.db, `INSERT INTO passkeys (id, user_id, credential_id, credential, name, created_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (credential_id) DO NOTHING`,
		k.ID, userID, credentialID, string(credential), name, k.CreatedAt)
	if err != nil {
		return k, err
	}
	got, err := s.PasskeyByCredential(ctx, credentialID)
	if err == nil && got.ID != k.ID {
		return k, ErrExists
	}
	return k, err
}

// ListPasskeys returns a user's passkeys, oldest first.
func (s *Store) ListPasskeys(ctx context.Context, userID string) ([]Passkey, error) {
	rows, err := s.query(ctx, s.db, `SELECT `+passkeyColumns+` FROM passkeys WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Passkey
	for rows.Next() {
		k, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// PasskeyByCredential finds a passkey by its credential ID.
func (s *Store) PasskeyByCredential(ctx context.Context, credentialID string) (Passkey, error) {
	return scanPasskey(s.queryRow(ctx, s.db, `SELECT `+passkeyColumns+` FROM passkeys WHERE credential_id = ?`, credentialID))
}

// UsePasskey records a sign-in with a passkey and its updated record (sign count).
func (s *Store) UsePasskey(ctx context.Context, credentialID string, credential []byte) error {
	res, err := s.exec(ctx, s.db, `UPDATE passkeys SET credential = ?, last_used_at = ? WHERE credential_id = ?`,
		string(credential), s.now(), credentialID)
	return expectOne(res, err)
}

// RenamePasskey names one of a user's passkeys.
func (s *Store) RenamePasskey(ctx context.Context, userID, id, name string) error {
	res, err := s.exec(ctx, s.db, `UPDATE passkeys SET name = ? WHERE user_id = ? AND id = ?`, name, userID, id)
	return expectOne(res, err)
}

// DeletePasskey removes one of a user's passkeys.
func (s *Store) DeletePasskey(ctx context.Context, userID, id string) error {
	res, err := s.exec(ctx, s.db, `DELETE FROM passkeys WHERE user_id = ? AND id = ?`, userID, id)
	return expectOne(res, err)
}

// DeletePasskeys removes every passkey of a user (an admin resetting their sign-in).
func (s *Store) DeletePasskeys(ctx context.Context, userID string) error {
	_, err := s.exec(ctx, s.db, `DELETE FROM passkeys WHERE user_id = ?`, userID)
	return err
}

// SaveWebAuthnSession keeps a passkey ceremony's state under id (a hash) for ttl, and
// drops expired ones.
func (s *Store) SaveWebAuthnSession(ctx context.Context, id string, data []byte, ttl time.Duration) error {
	now := s.now()
	if _, err := s.exec(ctx, s.db, `DELETE FROM webauthn_sessions WHERE expires_at < ?`, now); err != nil {
		return err
	}
	_, err := s.exec(ctx, s.db, `INSERT INTO webauthn_sessions (id, data, expires_at) VALUES (?, ?, ?)`, id, string(data), now.Add(ttl))
	return err
}

// TakeWebAuthnSession returns a ceremony's state and forgets it: each challenge is
// answered once.
func (s *Store) TakeWebAuthnSession(ctx context.Context, id string) ([]byte, error) {
	var data string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := s.queryRow(ctx, tx, `SELECT data FROM webauthn_sessions WHERE id = ? AND expires_at > ?`, id, s.now()).Scan(&data); err != nil {
			return notFound(err)
		}
		_, err := s.exec(ctx, tx, `DELETE FROM webauthn_sessions WHERE id = ?`, id)
		return err
	})
	return []byte(data), err
}
