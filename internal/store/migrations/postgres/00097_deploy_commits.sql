-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The commit a GitOps application had synced when a workload moved to a new version, and the pull request that
-- merged it, as the forge says: the monthly report names it for upgrades Goliash did not open itself (Renovate's,
-- a person's).
CREATE TABLE deploy_commits (
    id             TEXT PRIMARY KEY,
    org_id         TEXT NOT NULL,
    workspace_id   TEXT NOT NULL,
    service_id     TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    to_version     TEXT NOT NULL,
    repo           TEXT NOT NULL,
    revision       TEXT NOT NULL,
    deployed_at    TIMESTAMP NOT NULL,
    pull_url       TEXT NOT NULL DEFAULT '',
    pull_title     TEXT NOT NULL DEFAULT '',
    pull_by        TEXT NOT NULL DEFAULT '',
    pull_merged_at TIMESTAMP,
    looked_up_at   TIMESTAMP
);
CREATE UNIQUE INDEX deploy_commits_once ON deploy_commits (workspace_id, service_id, environment_id, to_version, revision);
CREATE INDEX deploy_commits_pending ON deploy_commits (looked_up_at, deployed_at);

-- +goose Down
DROP TABLE deploy_commits;
