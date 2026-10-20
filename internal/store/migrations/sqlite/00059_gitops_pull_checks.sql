-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- What the checks (CI) of a pull request Goliash opened say, as its forge last said: passing, failing, pending.
ALTER TABLE gitops_pulls ADD COLUMN checks TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE gitops_pulls DROP COLUMN checks;
