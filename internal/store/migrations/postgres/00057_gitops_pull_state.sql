-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Whether a pull request Goliash opened is still open, merged or closed, as its forge last said.
ALTER TABLE gitops_pulls ADD COLUMN state TEXT NOT NULL DEFAULT 'open';
ALTER TABLE gitops_pulls ADD COLUMN checked_at TIMESTAMP;

-- +goose Down
ALTER TABLE gitops_pulls DROP COLUMN checked_at;
ALTER TABLE gitops_pulls DROP COLUMN state;
