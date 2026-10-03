// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// A value encrypted with the secret key is stored as {"sealed":"v1:<base64>"} (AES-256-GCM,
// the nonce first), which is valid JSON for PostgreSQL's jsonb. Other values are
// plaintext from before a key was set.
const sealedVersion = "v1:"

type sealedJSON struct {
	Sealed string `json:"sealed"`
}

// sealed returns the ciphertext of a stored value, or false for plaintext. It parses
// the JSON because PostgreSQL's jsonb reformats it.
func sealed(stored string) (string, bool) {
	var v map[string]json.RawMessage
	if json.Unmarshal([]byte(stored), &v) != nil || len(v) != 1 || v["sealed"] == nil {
		return "", false
	}
	var enc string
	if json.Unmarshal(v["sealed"], &enc) != nil {
		return "", false
	}
	return strings.CutPrefix(enc, sealedVersion)
}

// SecretKeySize is the length of the key that encrypts secrets at rest.
const SecretKeySize = 32

// ErrNoSecretKey means a value is encrypted but the store has no key to open it.
var ErrNoSecretKey = errors.New("encrypted with a secret key the server was not given (GOLIASH_SECRET_KEY)")

// SetSecretKey makes the store encrypt notification channel settings (webhook URLs,
// signing secrets) at rest. Values are bound to their row, so a ciphertext copied to
// another row does not open.
func (s *Store) SetSecretKey(key []byte) error {
	if len(key) != SecretKeySize {
		return fmt.Errorf("secret key must be %d bytes, got %d", SecretKeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	s.aead = aead
	return nil
}

// Encrypts reports whether secrets are encrypted at rest.
func (s *Store) Encrypts() bool { return s.aead != nil }

// seal encrypts plaintext for the row id; without a key it returns plaintext.
func (s *Store) seal(id, plaintext string) string {
	if s.aead == nil {
		return plaintext
	}
	nonce := make([]byte, s.aead.NonceSize())
	_, _ = rand.Read(nonce) // crypto/rand.Read never fails
	b, _ := json.Marshal(sealedJSON{Sealed: sealedVersion + base64.StdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, []byte(plaintext), []byte(id)))})
	return string(b)
}

// open decrypts a sealed value of the row id; plaintext passes through.
func (s *Store) open(id, stored string) (string, error) {
	enc, ok := sealed(stored)
	if !ok {
		return stored, nil
	}
	if s.aead == nil {
		return "", ErrNoSecretKey
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", errors.New("encrypted value is damaged")
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(id))
	if err != nil {
		return "", errors.New("encrypted value does not open with this secret key")
	}
	return string(plain), nil
}

// SealSecrets encrypts channel settings stored before a secret key was set. It
// returns how many it encrypted.
func (s *Store) SealSecrets(ctx context.Context) (int, error) {
	if s.aead == nil {
		return 0, nil
	}
	all, err := s.channelConfigs(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range all {
		if _, ok := sealed(r.config); ok {
			continue
		}
		if _, err := s.exec(ctx, s.db, `UPDATE notification_channels SET config = ? WHERE id = ?`, s.seal(r.id, r.config), r.id); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

type storedConfig struct{ id, config string }

// channelConfigs reads every channel's stored settings, across workspaces.
func (s *Store) channelConfigs(ctx context.Context) ([]storedConfig, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, config FROM notification_channels`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []storedConfig
	for rows.Next() {
		var r storedConfig
		if err := rows.Scan(&r.id, &r.config); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CheckSecrets fails when stored secrets cannot be opened: encrypted values without a
// key, or with a different key. The server checks this at start, not at the first
// notification.
func (s *Store) CheckSecrets(ctx context.Context) error {
	all, err := s.channelConfigs(ctx)
	if err != nil {
		return err
	}
	for _, r := range all {
		if _, ok := sealed(r.config); ok {
			_, err := s.open(r.id, r.config)
			return err
		}
	}
	return nil
}
