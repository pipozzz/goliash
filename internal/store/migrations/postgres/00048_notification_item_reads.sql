-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The notifications each person opened one by one under the bell (message: its content key and when it was sent).
CREATE TABLE notification_item_reads (
    user_id       TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    message       TEXT NOT NULL,
    read_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, workspace_id, message)
);

-- +goose Down
DROP TABLE notification_item_reads;
