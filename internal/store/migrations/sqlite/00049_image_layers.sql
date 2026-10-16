-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- An image's layers, bottom first, with their sizes and the build steps that made them (JSON), to show what
-- made an image bigger.
ALTER TABLE image_evidence ADD COLUMN layers TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE image_evidence DROP COLUMN layers;
