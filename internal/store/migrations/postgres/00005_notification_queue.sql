-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- One notification waiting for (or done with) delivery through one rule's channel.
-- Instant items are due right away; digest items when the rule's next digest is due.
CREATE TABLE notification_queue (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    rule_id       TEXT NOT NULL REFERENCES notification_rules (id) ON DELETE CASCADE,
    payload       JSONB NOT NULL,
    dedup_key     TEXT NOT NULL,
    due_at        TIMESTAMPTZ NOT NULL,
    attempts      INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT NOT NULL DEFAULT '',
    sent_at       TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL,
    UNIQUE (rule_id, dedup_key)
);
CREATE INDEX notification_queue_due_idx ON notification_queue (due_at) WHERE sent_at IS NULL;

-- +goose Down
DROP TABLE notification_queue;
