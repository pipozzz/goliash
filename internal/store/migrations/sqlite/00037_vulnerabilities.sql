-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The packages (package URLs, as a JSON array) the SBOM attestation of a running image lists.
CREATE TABLE image_sboms (
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    repo          TEXT NOT NULL,
    digest        TEXT NOT NULL,
    purls         TEXT NOT NULL DEFAULT '[]',
    error         TEXT NOT NULL DEFAULT '',
    fetched_at    TIMESTAMP NOT NULL,
    PRIMARY KEY (workspace_id, repo, digest)
);

-- Known vulnerabilities per package URL, from OSV: public data, shared by every workspace.
CREATE TABLE osv_packages (
    purl        TEXT PRIMARY KEY,
    vulns       TEXT NOT NULL DEFAULT '[]',
    checked_at  TIMESTAMP NOT NULL
);

CREATE TABLE osv_vulns (
    id          TEXT PRIMARY KEY,
    aliases     TEXT NOT NULL DEFAULT '[]',
    summary     TEXT NOT NULL DEFAULT '',
    severity    TEXT NOT NULL DEFAULT '',
    fetched_at  TIMESTAMP NOT NULL
);

-- +goose Down
DROP TABLE osv_vulns;
DROP TABLE osv_packages;
DROP TABLE image_sboms;
