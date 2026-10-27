-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Patches the platform has waiting for a managed service (RDS pending maintenance and the like), one per line.
ALTER TABLE instances ADD COLUMN maintenance TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE instances DROP COLUMN maintenance;
