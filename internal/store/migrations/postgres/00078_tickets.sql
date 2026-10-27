-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Tickets filed in issue trackers: one per channel and upgrade (service, environment, application), open until the
-- upgrade is deployed.
CREATE TABLE tickets (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL,
    channel_id      TEXT NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    service_id      TEXT NOT NULL,
    environment_id  TEXT NOT NULL,
    app             TEXT NOT NULL DEFAULT '',
    external_id     TEXT NOT NULL DEFAULT '',
    external_key    TEXT NOT NULL DEFAULT '',
    url             TEXT NOT NULL DEFAULT '',
    title           TEXT NOT NULL DEFAULT '',
    last_note       TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL,
    closed_at       TIMESTAMPTZ
);
CREATE INDEX tickets_open_idx ON tickets (workspace_id, channel_id, service_id, environment_id, app);

-- +goose Down
DROP TABLE tickets;
