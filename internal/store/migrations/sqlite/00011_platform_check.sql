-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- Platforms are validated by the server (against the agent protocol), so a new platform
-- needs no migration. SQLite cannot drop a CHECK constraint, so the targets table is
-- rebuilt once more, with foreign keys off as in migration 9.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
-- +goose StatementBegin
BEGIN;
CREATE TABLE targets_new (
    id                     TEXT PRIMARY KEY,
    org_id                 TEXT NOT NULL,
    workspace_id           TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    environment_id         TEXT NOT NULL REFERENCES environments (id),
    agent_id               TEXT REFERENCES agents (id) ON DELETE SET NULL,
    platform               TEXT NOT NULL,
    name                   TEXT NOT NULL,
    settings               TEXT NOT NULL DEFAULT '{}',
    poll_interval_seconds  INTEGER NOT NULL DEFAULT 300,
    last_snapshot_at       TIMESTAMP,
    created_at             TIMESTAMP NOT NULL,
    collector_status       TEXT NOT NULL DEFAULT '',
    collector_error        TEXT NOT NULL DEFAULT '',
    collector_reported_at  TIMESTAMP,
    UNIQUE (workspace_id, name)
);
INSERT INTO targets_new (id, org_id, workspace_id, environment_id, agent_id, platform, name, settings,
    poll_interval_seconds, last_snapshot_at, created_at, collector_status, collector_error, collector_reported_at)
SELECT id, org_id, workspace_id, environment_id, agent_id, platform, name, settings,
    poll_interval_seconds, last_snapshot_at, created_at, collector_status, collector_error, collector_reported_at
FROM targets;
DROP TABLE targets;
ALTER TABLE targets_new RENAME TO targets;
CREATE INDEX targets_agent_idx ON targets (agent_id);
COMMIT;
-- +goose StatementEnd
PRAGMA foreign_keys = ON;

-- +goose Down
DELETE FROM targets WHERE platform NOT IN ('kubernetes', 'ecs', 'nomad', 'swarm', 'docker');
