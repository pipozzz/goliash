-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Open drifts have resolved_at NULL; at most one open drift per service, environment and kind.
CREATE TABLE drifts (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    service_id      TEXT NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    environment_id  TEXT NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    kind            TEXT NOT NULL CHECK (kind IN ('env', 'upstream', 'inconsistent')),
    detail          TEXT NOT NULL DEFAULT '{}',
    since           TIMESTAMP NOT NULL,
    resolved_at     TIMESTAMP
);
CREATE UNIQUE INDEX drifts_open_idx ON drifts (service_id, environment_id, kind) WHERE resolved_at IS NULL;
CREATE INDEX drifts_workspace_idx ON drifts (workspace_id, resolved_at);

-- Result of the last upstream tag check of a service.
ALTER TABLE services ADD COLUMN upstream_checked_at TIMESTAMP;
ALTER TABLE services ADD COLUMN upstream_error TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE services DROP COLUMN upstream_error;
ALTER TABLE services DROP COLUMN upstream_checked_at;
DROP TABLE drifts;
