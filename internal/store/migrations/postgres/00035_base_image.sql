-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The image a service's upstream image was built on (org.opencontainers.image.base.name), read with its source.
ALTER TABLE services ADD COLUMN base_image TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE services DROP COLUMN base_image;
