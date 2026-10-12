-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- A workspace may map workloads of new images to the service Goliash suggests, as soon as they report, with a
-- mapping rule per image like the inbox makes.
ALTER TABLE workspaces ADD COLUMN auto_map INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE workspaces DROP COLUMN auto_map;
