-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Reading an image without an SBOM learned the version of a Go program's own module (Traefik's, built from its
-- release's checkout): the images read so that hold a Go executable are read again, once.
UPDATE image_sboms SET fetched_at = '2000-01-01 00:00:00+00' WHERE source = 'packages' AND purls LIKE '%pkg:golang/stdlib@%';

-- +goose Down
SELECT 1;
