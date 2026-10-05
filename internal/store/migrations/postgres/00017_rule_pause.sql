-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- A paused notification rule queues nothing until it is resumed.
ALTER TABLE notification_rules ADD COLUMN paused BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE notification_rules DROP COLUMN paused;
