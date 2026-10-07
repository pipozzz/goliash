-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Passkeys (WebAuthn credentials): the credential record as JSON, found by its ID.
CREATE TABLE passkeys (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    credential_id  TEXT NOT NULL UNIQUE,
    credential     TEXT NOT NULL,
    name           TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL,
    last_used_at   TIMESTAMP
);
CREATE INDEX passkeys_user_idx ON passkeys (user_id);

-- A passkey ceremony in progress: the challenge the browser has to sign.
CREATE TABLE webauthn_sessions (
    id          TEXT PRIMARY KEY,
    data        TEXT NOT NULL,
    expires_at  TIMESTAMP NOT NULL
);

-- +goose Down
DROP TABLE webauthn_sessions;
DROP TABLE passkeys;
