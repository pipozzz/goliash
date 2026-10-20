-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Change freezes: periods when a workspace takes no changes it does not have to (holidays, a big sale).
CREATE TABLE change_freezes (
    id           TEXT PRIMARY KEY,
    org_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    starts_at    TIMESTAMP NOT NULL,
    ends_at      TIMESTAMP NOT NULL,
    note         TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL
);
CREATE INDEX change_freezes_workspace ON change_freezes (org_id, workspace_id, ends_at);

-- +goose Down
DROP TABLE change_freezes;
