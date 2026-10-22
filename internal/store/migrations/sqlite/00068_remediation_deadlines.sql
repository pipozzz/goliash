-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- A workspace's remediation deadlines, as JSON: {"exploited":7,"security":14,"eol":30} days; "" when it has none.
ALTER TABLE workspaces ADD COLUMN remediation TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE workspaces DROP COLUMN remediation;
