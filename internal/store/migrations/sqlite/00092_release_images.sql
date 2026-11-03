-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The image behind a release that runs nowhere yet (the version an upgrade would move to): the digest its tag
-- pointed at when read, so its packages (in image_sboms) tell what upgrading fixes before anyone deploys it.
CREATE TABLE release_images (
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    repo          TEXT NOT NULL,
    tag           TEXT NOT NULL,
    digest        TEXT NOT NULL,
    read_at       TIMESTAMP NOT NULL,
    PRIMARY KEY (workspace_id, repo, tag)
);

-- +goose Down
DROP TABLE release_images;
