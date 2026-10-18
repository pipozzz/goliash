-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The GitOps application that deploys a workload, as its target's latest snapshot reports it (an Argo CD
-- Application listing the workload): where it deploys from, the commit last synced, sync and health.
CREATE TABLE workload_gitops (
    target_id       TEXT NOT NULL REFERENCES targets (id) ON DELETE CASCADE,
    workload_id     TEXT NOT NULL,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL,
    tool            TEXT NOT NULL,
    app             TEXT NOT NULL,
    app_namespace   TEXT NOT NULL DEFAULT '',
    project         TEXT NOT NULL DEFAULT '',
    repo            TEXT NOT NULL DEFAULT '',
    path            TEXT NOT NULL DEFAULT '',
    target_revision TEXT NOT NULL DEFAULT '',
    revision        TEXT NOT NULL DEFAULT '',
    sync            TEXT NOT NULL DEFAULT '',
    health          TEXT NOT NULL DEFAULT '',
    sync_since      TIMESTAMP NOT NULL,
    updated_at      TIMESTAMP NOT NULL,
    PRIMARY KEY (target_id, workload_id)
);
CREATE INDEX workload_gitops_workspace ON workload_gitops (workspace_id);

-- +goose Down
DROP TABLE workload_gitops;
