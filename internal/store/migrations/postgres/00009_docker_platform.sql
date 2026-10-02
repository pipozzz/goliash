-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
ALTER TABLE targets DROP CONSTRAINT targets_platform_check;
ALTER TABLE targets ADD CONSTRAINT targets_platform_check
    CHECK (platform IN ('kubernetes', 'ecs', 'nomad', 'swarm', 'docker'));

-- +goose Down
DELETE FROM targets WHERE platform = 'docker';
ALTER TABLE targets DROP CONSTRAINT targets_platform_check;
ALTER TABLE targets ADD CONSTRAINT targets_platform_check
    CHECK (platform IN ('kubernetes', 'ecs', 'nomad', 'swarm'));
