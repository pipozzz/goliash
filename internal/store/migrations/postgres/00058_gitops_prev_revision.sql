-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The commit a GitOps application synced before the one it runs, and when it moved: what changed in Git.
ALTER TABLE workload_gitops ADD COLUMN prev_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE workload_gitops ADD COLUMN revision_at TIMESTAMP;

-- +goose Down
ALTER TABLE workload_gitops DROP COLUMN revision_at;
ALTER TABLE workload_gitops DROP COLUMN prev_revision;
