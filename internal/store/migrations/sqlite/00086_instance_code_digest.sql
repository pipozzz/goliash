-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- What a .zip Lambda function version runs (its code and layer versions) as one digest: its packages are kept as an
-- SBOM under that digest.
ALTER TABLE instances ADD COLUMN code_digest TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE instances DROP COLUMN code_digest;
