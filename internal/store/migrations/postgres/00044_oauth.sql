-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- OAuth 2.1 for MCP clients: apps (Claude, Cursor, …) a person lets read Goliash as them, without an API token.
-- A client registered itself (metadata_url empty) or is described by its Client ID Metadata Document.
CREATE TABLE oauth_clients (
    id             TEXT PRIMARY KEY, -- the client_id: glsh_cli_… or the metadata document's https URL
    name           TEXT NOT NULL,
    redirect_uris  TEXT NOT NULL,    -- JSON array
    metadata_url   TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL,
    fetched_at     TIMESTAMPTZ
);

-- Authorization codes: single use, ten minutes, bound to the client, its redirect and its PKCE challenge.
CREATE TABLE oauth_codes (
    hash          TEXT PRIMARY KEY,
    client_id     TEXT NOT NULL,
    user_id       TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    redirect_uri  TEXT NOT NULL,
    challenge     TEXT NOT NULL,
    scope         TEXT NOT NULL,
    resource      TEXT NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL
);

-- A grant: one app reading one workspace as one person, with its current access and refresh tokens (hashed).
CREATE TABLE oauth_grants (
    id                  TEXT PRIMARY KEY,
    user_id             TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id        TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    client_id           TEXT NOT NULL,
    client_name         TEXT NOT NULL,
    scope               TEXT NOT NULL,
    resource            TEXT NOT NULL,
    access_hash         TEXT NOT NULL UNIQUE,
    access_expires_at   TIMESTAMPTZ NOT NULL,
    refresh_hash        TEXT NOT NULL UNIQUE,
    refresh_expires_at  TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL,
    last_used_at        TIMESTAMPTZ
);
CREATE INDEX oauth_grants_user_idx ON oauth_grants (user_id);

-- +goose Down
DROP TABLE oauth_grants;
DROP TABLE oauth_codes;
DROP TABLE oauth_clients;
