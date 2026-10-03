-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- Platforms are validated by the server (against the agent protocol), so a new platform
-- needs no migration.

-- +goose Up
ALTER TABLE targets DROP CONSTRAINT targets_platform_check;

-- +goose Down
DELETE FROM targets WHERE platform NOT IN ('kubernetes', 'ecs', 'nomad', 'swarm', 'docker');
ALTER TABLE targets ADD CONSTRAINT targets_platform_check
    CHECK (platform IN ('kubernetes', 'ecs', 'nomad', 'swarm', 'docker'));
