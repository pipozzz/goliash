-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Security watches: software a workspace keeps an eye on after an advance notice ("php: two critical fixes on
-- Oct 21"), until a date or until removed.
CREATE TABLE security_watches (
    id           TEXT PRIMARY KEY,
    org_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    term         TEXT NOT NULL,
    note         TEXT NOT NULL DEFAULT '',
    until_at     TIMESTAMP,
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL
);
CREATE INDEX security_watches_workspace ON security_watches (org_id, workspace_id);

-- +goose Down
DROP TABLE security_watches;
