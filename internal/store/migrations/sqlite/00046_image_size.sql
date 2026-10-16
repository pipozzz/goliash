-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- What pulling the image downloads (compressed layers and config), read from its manifest; 0 when unknown.
ALTER TABLE image_evidence ADD COLUMN size_bytes BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE image_evidence DROP COLUMN size_bytes;
