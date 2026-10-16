-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Is the image built on its base as the base's tag is now? base_status: '' unknown, current, outdated (built on an
-- older build of the base: FROM a cached copy, or another base); base_since: when the current base was built.
ALTER TABLE image_evidence ADD COLUMN base_image TEXT NOT NULL DEFAULT '';
ALTER TABLE image_evidence ADD COLUMN base_status TEXT NOT NULL DEFAULT '';
ALTER TABLE image_evidence ADD COLUMN base_since TIMESTAMPTZ;

-- +goose Down
ALTER TABLE image_evidence DROP COLUMN base_since;
ALTER TABLE image_evidence DROP COLUMN base_status;
ALTER TABLE image_evidence DROP COLUMN base_image;
