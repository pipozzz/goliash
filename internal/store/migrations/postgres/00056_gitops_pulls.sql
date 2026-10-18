-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Pull (merge) requests Goliash opened in GitOps repositories: one per workload and version, so a version is not
-- proposed twice, and the pages link the open one instead of offering another.
CREATE TABLE gitops_pulls (
    id           TEXT PRIMARY KEY,
    org_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    service_id   TEXT NOT NULL,
    environment  TEXT NOT NULL,
    target_id    TEXT NOT NULL,
    workload_id  TEXT NOT NULL,
    from_version TEXT NOT NULL,
    to_version   TEXT NOT NULL,
    url          TEXT NOT NULL,
    opened_by    TEXT NOT NULL,
    opened_at    TIMESTAMP NOT NULL
);
CREATE UNIQUE INDEX gitops_pulls_once ON gitops_pulls (workspace_id, target_id, workload_id, to_version);

-- +goose Down
DROP TABLE gitops_pulls;
