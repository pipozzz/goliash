-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- Drift kinds are validated by the server, so new kinds need no migration.

-- +goose Up
ALTER TABLE drifts DROP CONSTRAINT drifts_kind_check;

-- +goose Down
DELETE FROM drifts WHERE kind NOT IN ('env', 'upstream', 'inconsistent');
ALTER TABLE drifts ADD CONSTRAINT drifts_kind_check CHECK (kind IN ('env', 'upstream', 'inconsistent'));
