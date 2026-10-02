-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The environment an instance runs in: its target's, unless a goliash.env label says otherwise.
ALTER TABLE instances ADD COLUMN environment_id TEXT REFERENCES environments (id) ON DELETE SET NULL;
-- For unmapped instances (the inbox): the service name the heuristic proposes.
ALTER TABLE instances ADD COLUMN suggested_service TEXT NOT NULL DEFAULT '';
CREATE INDEX instances_target_idx ON instances (target_id);

-- +goose Down
DROP INDEX instances_target_idx;
ALTER TABLE instances DROP COLUMN suggested_service;
ALTER TABLE instances DROP COLUMN environment_id;
