-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Whether the workspace got its default security alerts (a bell rule): once, so deleting the rule keeps it gone.
ALTER TABLE workspaces ADD COLUMN default_alerts INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE workspaces DROP COLUMN default_alerts;
