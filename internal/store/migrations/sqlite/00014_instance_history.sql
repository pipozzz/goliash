-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- When each instance ran: one row per period, so "what ran in prod on 12 September at
-- 14:00" has an answer even after versions moved on and back. Instance rows are reused
-- when a version comes back, so they alone cannot tell.
CREATE TABLE instance_history (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    instance_id   TEXT NOT NULL REFERENCES instances (id) ON DELETE CASCADE,
    running       INTEGER NOT NULL DEFAULT 0,
    since         TIMESTAMP NOT NULL,
    until         TIMESTAMP
);
CREATE INDEX instance_history_period_idx ON instance_history (workspace_id, since, until);
CREATE INDEX instance_history_open_idx ON instance_history (instance_id) WHERE until IS NULL;

-- What is known so far: each instance from first seen until removed.
INSERT INTO instance_history (id, org_id, workspace_id, instance_id, running, since, until)
SELECT id, org_id, workspace_id, id, running, first_seen_at, removed_at FROM instances;

-- +goose Down
DROP TABLE instance_history;
