-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The license each package of an image's SBOM declares (JSON: purl -> SPDX expression), for license compliance.
-- NULL for SBOMs read before licenses were kept: they are read once more.
ALTER TABLE image_sboms ADD COLUMN licenses TEXT;

-- +goose Down
ALTER TABLE image_sboms DROP COLUMN licenses;
