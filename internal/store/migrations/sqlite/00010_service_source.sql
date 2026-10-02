-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The source repository an upstream image declares (org.opencontainers.image.source),
-- read from the registry. It gives services outside the catalog release notes.
ALTER TABLE services ADD COLUMN source_url TEXT NOT NULL DEFAULT '';
-- The image the source was read from; a different upstream is read again.
ALTER TABLE services ADD COLUMN source_image TEXT NOT NULL DEFAULT '';
ALTER TABLE services ADD COLUMN source_checked_at TIMESTAMP;

-- +goose Down
ALTER TABLE services DROP COLUMN source_checked_at;
ALTER TABLE services DROP COLUMN source_image;
ALTER TABLE services DROP COLUMN source_url;
