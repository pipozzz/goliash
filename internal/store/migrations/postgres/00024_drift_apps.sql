-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- A service used by several applications (one postgres image, many databases) drifts
-- per application: its versions are compared within each one. Empty: the whole service.
ALTER TABLE drifts ADD COLUMN app TEXT NOT NULL DEFAULT '';
DROP INDEX drifts_open_idx;
CREATE UNIQUE INDEX drifts_open_idx ON drifts (service_id, app, environment_id, kind) WHERE resolved_at IS NULL;
ALTER TABLE events ADD COLUMN app TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE events DROP COLUMN app;
DROP INDEX drifts_open_idx;
DELETE FROM drifts WHERE app <> '';
CREATE UNIQUE INDEX drifts_open_idx ON drifts (service_id, environment_id, kind) WHERE resolved_at IS NULL;
ALTER TABLE drifts DROP COLUMN app;
