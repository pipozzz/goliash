-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The bases of an image's base, down the chain, as JSON: each is built on the next, current or outdated.
ALTER TABLE image_evidence ADD COLUMN base_chain TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE image_evidence DROP COLUMN base_chain;
