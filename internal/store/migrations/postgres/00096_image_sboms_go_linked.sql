-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The Go packages an image's Go executables link, as Goliash read them (JSON list; null when not known, NULL when
-- the image was read before this was kept): a Go advisory about a package none links does not concern the image.
ALTER TABLE image_sboms ADD COLUMN go_linked TEXT;

-- +goose Down
ALTER TABLE image_sboms DROP COLUMN go_linked;
