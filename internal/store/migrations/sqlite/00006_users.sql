-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- People who sign in. Roles are per organization: owner, admin, member, viewer.
CREATE TABLE users (
    id             TEXT PRIMARY KEY,
    org_id         TEXT NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    email          TEXT NOT NULL,
    name           TEXT NOT NULL DEFAULT '',
    role           TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
    created_at     TIMESTAMP NOT NULL,
    last_login_at  TIMESTAMP,
    UNIQUE (org_id, email)
);

-- Browser sessions; id is the SHA-256 of the cookie value.
CREATE TABLE sessions (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at    TIMESTAMP NOT NULL,
    expires_at    TIMESTAMP NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

-- Single-use sign-in links (magic links); id is the SHA-256 of the token.
CREATE TABLE login_tokens (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at  TIMESTAMP NOT NULL,
    used_at     TIMESTAMP,
    created_at  TIMESTAMP NOT NULL
);

-- +goose Down
DROP TABLE login_tokens;
DROP TABLE sessions;
DROP TABLE users;
