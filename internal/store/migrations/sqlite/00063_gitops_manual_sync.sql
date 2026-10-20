-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- A GitOps application its tool syncs only by hand (Argo CD without an automated sync policy).
ALTER TABLE workload_gitops ADD COLUMN manual_sync BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE workload_gitops DROP COLUMN manual_sync;
