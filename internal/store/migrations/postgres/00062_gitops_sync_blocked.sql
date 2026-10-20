-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Why Argo CD's sync windows keep a GitOps application from syncing now, and until when.
ALTER TABLE workload_gitops ADD COLUMN sync_blocked TEXT NOT NULL DEFAULT '';
ALTER TABLE workload_gitops ADD COLUMN sync_blocked_until TIMESTAMP;

-- +goose Down
ALTER TABLE workload_gitops DROP COLUMN sync_blocked_until;
ALTER TABLE workload_gitops DROP COLUMN sync_blocked;
