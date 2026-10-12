-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Credentials a workspace gives the server for a private registry, so the server checks its repositories itself
-- instead of the agents. secret is encrypted with the server's secret key, bound to the row id.
CREATE TABLE registry_credentials (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    host            TEXT NOT NULL,
    username        TEXT NOT NULL,
    secret          TEXT NOT NULL,
    created_by      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL,
    used_at         TIMESTAMP,
    error           TEXT NOT NULL DEFAULT '',
    UNIQUE (workspace_id, host)
);

-- +goose Down
DROP TABLE registry_credentials;
