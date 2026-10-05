// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
)

// TOTP is a user's two-factor state.
type TOTP struct {
	Secret   string // base32; empty when none is set up
	Enabled  bool   // false while set up but not confirmed
	LastStep int64  // the last time step accepted
	Codes    int    // unused recovery codes
}

func totpID(userID string) string { return "totp:" + userID }

// UserTOTP returns a user's two-factor state, the secret opened.
func (s *Store) UserTOTP(ctx context.Context, userID string) (TOTP, error) {
	var t TOTP
	var stored string
	var enabled sql.NullTime
	if err := s.queryRow(ctx, s.db, `SELECT totp_secret, totp_enabled_at, totp_last_step FROM users WHERE id = ?`, userID).
		Scan(&stored, &enabled, &t.LastStep); err != nil {
		return t, notFound(err)
	}
	secret, err := s.open(totpID(userID), stored)
	if err != nil {
		return t, err
	}
	t.Secret, t.Enabled = secret, enabled.Valid
	err = s.queryRow(ctx, s.db, `SELECT COUNT(*) FROM recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID).Scan(&t.Codes)
	return t, err
}

// StartTOTP stores a new secret that is not in force until EnableTOTP. It refuses
// (ErrExists) while two-factor sign-in is on.
func (s *Store) StartTOTP(ctx context.Context, userID, secret string) error {
	res, err := s.exec(ctx, s.db, `UPDATE users SET totp_secret = ?, totp_last_step = 0 WHERE id = ? AND totp_enabled_at IS NULL`,
		s.seal(totpID(userID), secret), userID)
	if err := expectOne(res, err); err != nil {
		return ErrExists
	}
	return nil
}

// EnableTOTP turns two-factor sign-in on with the stored secret, records the step of
// the confirming code and replaces the recovery codes (hashes).
func (s *Store) EnableTOTP(ctx context.Context, userID string, step int64, codeHashes []string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := s.exec(ctx, tx, `UPDATE users SET totp_enabled_at = ?, totp_last_step = ? WHERE id = ? AND totp_secret <> ''`,
			s.now(), step, userID)
		if err := expectOne(res, err); err != nil {
			return err
		}
		return s.replaceCodes(ctx, tx, userID, codeHashes)
	})
}

// DisableTOTP turns two-factor sign-in off and forgets the secret and recovery codes.
func (s *Store) DisableTOTP(ctx context.Context, userID string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := s.exec(ctx, tx, `UPDATE users SET totp_secret = '', totp_enabled_at = NULL, totp_last_step = 0 WHERE id = ?`, userID)
		if err := expectOne(res, err); err != nil {
			return err
		}
		_, err = s.exec(ctx, tx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID)
		return err
	})
}

// UseTOTPStep records an accepted time step. It reports false when the step (or a
// later one) was used already: a code works once.
func (s *Store) UseTOTPStep(ctx context.Context, userID string, step int64) (bool, error) {
	res, err := s.exec(ctx, s.db, `UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, userID, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// UseRecoveryCode uses up a recovery code (by hash). It reports false for an unknown
// or used code.
func (s *Store) UseRecoveryCode(ctx context.Context, userID, hash string) (bool, error) {
	res, err := s.exec(ctx, s.db, `UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND hash = ? AND used_at IS NULL`,
		s.now(), userID, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ReplaceRecoveryCodes gives a user new recovery codes; the old ones stop working.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID string, hashes []string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error { return s.replaceCodes(ctx, tx, userID, hashes) })
}

func (s *Store) replaceCodes(ctx context.Context, tx *sql.Tx, userID string, hashes []string) error {
	if _, err := s.exec(ctx, tx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	now := s.now()
	for _, h := range hashes {
		if _, err := s.exec(ctx, tx, `INSERT INTO recovery_codes (hash, user_id, created_at) VALUES (?, ?, ?)`, h, userID, now); err != nil {
			return err
		}
	}
	return nil
}
