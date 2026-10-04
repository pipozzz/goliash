-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- Drift kinds are validated by the server, so new kinds (declared, eol) need no
-- migration. SQLite cannot drop a CHECK constraint, so the table is rebuilt; nothing
-- references drifts, so foreign keys can stay on.

-- +goose Up
CREATE TABLE drifts_new (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    service_id      TEXT NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    environment_id  TEXT NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    kind            TEXT NOT NULL,
    detail          TEXT NOT NULL DEFAULT '{}',
    since           TIMESTAMP NOT NULL,
    resolved_at     TIMESTAMP,
    notified_at     TIMESTAMP
);
INSERT INTO drifts_new (id, org_id, workspace_id, service_id, environment_id, kind, detail, since, resolved_at, notified_at)
SELECT id, org_id, workspace_id, service_id, environment_id, kind, detail, since, resolved_at, notified_at FROM drifts;
DROP TABLE drifts;
ALTER TABLE drifts_new RENAME TO drifts;
CREATE UNIQUE INDEX drifts_open_idx ON drifts (service_id, environment_id, kind) WHERE resolved_at IS NULL;
CREATE INDEX drifts_workspace_idx ON drifts (workspace_id, resolved_at);

-- +goose Down
DELETE FROM drifts WHERE kind NOT IN ('env', 'upstream', 'inconsistent');
