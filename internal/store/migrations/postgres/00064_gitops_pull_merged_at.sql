-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- When Goliash first saw a pull request it opened merged: from then on its version should be on its way.
ALTER TABLE gitops_pulls ADD COLUMN merged_at TIMESTAMP;

-- +goose Down
ALTER TABLE gitops_pulls DROP COLUMN merged_at;
