-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The OpenVEX statements attached to an image by its publisher, as JSON: which vulnerabilities its packages match
-- but do not affect it.
CREATE TABLE image_vex (
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL,
    repo          TEXT NOT NULL,
    digest        TEXT NOT NULL,
    statements    TEXT NOT NULL,
    error         TEXT NOT NULL DEFAULT '',
    fetched_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workspace_id, repo, digest)
);

-- +goose Down
DROP TABLE image_vex;
