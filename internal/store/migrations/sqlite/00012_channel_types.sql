-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- Channel types are validated by the server, so new types (Discord, Telegram, ntfy) need
-- no migration. SQLite cannot drop a CHECK constraint, so the table is rebuilt with foreign
-- keys off, as in migration 9.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
-- +goose StatementBegin
BEGIN;
CREATE TABLE notification_channels_new (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    type          TEXT NOT NULL,
    name          TEXT NOT NULL,
    config        TEXT NOT NULL DEFAULT '{}',
    created_at    TIMESTAMP NOT NULL,
    UNIQUE (workspace_id, name)
);
INSERT INTO notification_channels_new (id, org_id, workspace_id, type, name, config, created_at)
SELECT id, org_id, workspace_id, type, name, config, created_at FROM notification_channels;
DROP TABLE notification_channels;
ALTER TABLE notification_channels_new RENAME TO notification_channels;
COMMIT;
-- +goose StatementEnd
PRAGMA foreign_keys = ON;

-- +goose Down
DELETE FROM notification_channels WHERE type NOT IN ('slack', 'webhook', 'email');
