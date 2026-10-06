-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- An application's team: services in it without an owner get this one, including
-- services that appear later. app is the name the matrix shows (after renames).
CREATE TABLE app_teams (
    org_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    app          TEXT NOT NULL,
    team         TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workspace_id, app)
);

-- +goose Down
DROP TABLE app_teams;
