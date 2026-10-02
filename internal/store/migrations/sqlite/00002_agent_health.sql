-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Set when an agent misses heartbeats past the threshold; cleared on the next heartbeat.
ALTER TABLE agents ADD COLUMN stale_since TIMESTAMP;

-- Collector health per target, as last reported in a heartbeat.
ALTER TABLE targets ADD COLUMN collector_status TEXT NOT NULL DEFAULT '';
ALTER TABLE targets ADD COLUMN collector_error TEXT NOT NULL DEFAULT '';
ALTER TABLE targets ADD COLUMN collector_reported_at TIMESTAMP;

-- +goose Down
ALTER TABLE targets DROP COLUMN collector_reported_at;
ALTER TABLE targets DROP COLUMN collector_error;
ALTER TABLE targets DROP COLUMN collector_status;
ALTER TABLE agents DROP COLUMN stale_since;
