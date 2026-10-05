-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Two-factor sign-in: a TOTP secret (sealed like channel secrets; empty = off), when it
-- was turned on, and single-use recovery codes stored as SHA-256 hashes.
ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT '';
-- totp_last_step refuses a code twice (replay).
ALTER TABLE users ADD COLUMN totp_last_step BIGINT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_enabled_at TIMESTAMP;
CREATE TABLE recovery_codes (
    hash        TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    used_at     TIMESTAMP,
    created_at  TIMESTAMP NOT NULL
);
CREATE INDEX recovery_codes_user_idx ON recovery_codes (user_id);
-- What a sign-in token is for: link (e-mailed or printed), second-factor (after a
-- password or link, waiting for the code), recovery (GOLIASH_RECOVERY_EMAIL; skips
-- the second factor).
ALTER TABLE login_tokens ADD COLUMN purpose TEXT NOT NULL DEFAULT 'link';
ALTER TABLE login_tokens ADD COLUMN method TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE login_tokens DROP COLUMN method;
ALTER TABLE login_tokens DROP COLUMN purpose;
DROP TABLE recovery_codes;
ALTER TABLE users DROP COLUMN totp_enabled_at;
ALTER TABLE users DROP COLUMN totp_last_step;
ALTER TABLE users DROP COLUMN totp_secret;
