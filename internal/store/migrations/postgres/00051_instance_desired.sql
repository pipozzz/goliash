-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- How many replicas the instance's workload asks for, as the platform reports it (NULL: unknown, e.g. a cron job),
-- to tell a rollout that does not finish.
ALTER TABLE instances ADD COLUMN desired INTEGER;

-- +goose Down
ALTER TABLE instances DROP COLUMN desired;
