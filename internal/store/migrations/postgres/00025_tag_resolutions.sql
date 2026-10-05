-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The exact version a moving tag ("1", "18-alpine", "latest") stood for, found by
-- matching the running image's digest with the registry's tags. version is empty
-- when no tag matched; checked_at says when to try again.
CREATE TABLE tag_resolutions (
    org_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    repo         TEXT NOT NULL,
    digest       TEXT NOT NULL,
    version      TEXT NOT NULL DEFAULT '',
    checked_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workspace_id, repo, digest)
);

-- +goose Down
DROP TABLE tag_resolutions;
