-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The application a workload belongs to, read from its labels at ingest (Helm release,
-- app.kubernetes.io/part-of, Compose project, …), and which label it came from. A
-- workspace may name its own label key first.
ALTER TABLE instances ADD COLUMN app TEXT NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN app_source TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN app_label TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE workspaces DROP COLUMN app_label;
ALTER TABLE instances DROP COLUMN app_source;
ALTER TABLE instances DROP COLUMN app;
