-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- When each person last looked at their notifications in a workspace: what came after is unread.
CREATE TABLE notification_reads (
    user_id       TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    seen_at       TIMESTAMP NOT NULL,
    PRIMARY KEY (user_id, workspace_id)
);

-- +goose Down
DROP TABLE notification_reads;
