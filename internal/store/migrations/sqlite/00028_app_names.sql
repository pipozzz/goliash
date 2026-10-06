-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Applications are found from labels and namespaces; people rename or merge them here:
-- every workload whose application is name is shown under shown_as.
CREATE TABLE app_names (
    org_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    shown_as     TEXT NOT NULL,
    created_at   TIMESTAMP NOT NULL,
    PRIMARY KEY (workspace_id, name)
);
-- A service placed in one application by hand, whatever its workloads' labels say.
ALTER TABLE services ADD COLUMN app TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE services DROP COLUMN app;
DROP TABLE app_names;
