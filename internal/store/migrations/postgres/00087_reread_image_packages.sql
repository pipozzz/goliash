-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Reading an image without an SBOM learned to find the Go executables a vendor's package puts in usr/bin, and no
-- longer misreads a Go version from bytes that only look like build information: every image read so is read
-- again, once (NeedsAppPackages takes a read this old for one from before the application's packages were read).
UPDATE image_sboms SET fetched_at = '2000-01-01 00:00:00+00' WHERE source = 'packages';

-- +goose Down
SELECT 1;
