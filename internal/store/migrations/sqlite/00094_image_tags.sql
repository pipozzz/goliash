-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- When each tag of a private repository was pushed, as its agent read it (Amazon ECR): what orders builds tagged
-- with commit hashes, which no version does.
CREATE TABLE image_tags (
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    repo          TEXT NOT NULL,
    tag           TEXT NOT NULL,
    pushed_at     TIMESTAMP NOT NULL,
    PRIMARY KEY (workspace_id, repo, tag)
);

-- +goose Down
DROP TABLE image_tags;
