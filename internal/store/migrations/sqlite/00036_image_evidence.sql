-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Signatures and attestations found for running images, by repository and digest: that they exist,
-- not that they verify. error is the last lookup's failure, if any.
CREATE TABLE image_evidence (
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    repo          TEXT NOT NULL,
    digest        TEXT NOT NULL,
    signed        BOOLEAN NOT NULL DEFAULT FALSE,
    sbom          BOOLEAN NOT NULL DEFAULT FALSE,
    provenance    BOOLEAN NOT NULL DEFAULT FALSE,
    found         TEXT NOT NULL DEFAULT '[]',
    error         TEXT NOT NULL DEFAULT '',
    checked_at    TIMESTAMP NOT NULL,
    PRIMARY KEY (workspace_id, repo, digest)
);

-- +goose Down
DROP TABLE image_evidence;
