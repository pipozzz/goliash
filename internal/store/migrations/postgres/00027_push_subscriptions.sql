-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Browsers subscribed to a web push channel. keys holds the browser's p256dh key and
-- auth secret, sealed like channel settings; endpoint is the push service URL.
CREATE TABLE push_subscriptions (
    id           TEXT PRIMARY KEY,
    org_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    channel_id   TEXT NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    endpoint     TEXT NOT NULL,
    keys         TEXT NOT NULL,
    label        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (channel_id, endpoint)
);

-- Server-wide secrets the server makes itself, such as the web push (VAPID) key pair;
-- values are sealed like channel settings.
CREATE TABLE server_secrets (
    name  TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose Down
DROP TABLE server_secrets;
DROP TABLE push_subscriptions;
