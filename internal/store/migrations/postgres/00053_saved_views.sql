-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Saved views: a name for a matrix or tiles address with its filters, a person's own (user_id) or the workspace's
-- (user_id NULL), shown as tabs; a person's home can be one ("view:<id>" in users.home_view).
CREATE TABLE saved_views (
    id           TEXT PRIMARY KEY,
    org_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    user_id      TEXT,
    name         TEXT NOT NULL,
    url          TEXT NOT NULL,
    created_at   TIMESTAMP NOT NULL
);
CREATE INDEX saved_views_workspace ON saved_views (workspace_id, user_id);

-- +goose Down
DROP TABLE saved_views;
