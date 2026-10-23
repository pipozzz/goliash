-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- An image's dev.goliash.components label: software built into it that no package database lists.
ALTER TABLE image_evidence ADD COLUMN components TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE image_evidence DROP COLUMN components;
