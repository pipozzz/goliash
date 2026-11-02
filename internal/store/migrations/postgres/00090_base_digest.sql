-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The digest of the base's current build an image was compared with: its packages say which of the image's
-- vulnerabilities a rebuild on it fixes.
ALTER TABLE image_evidence ADD COLUMN base_digest TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE image_evidence DROP COLUMN base_digest;
