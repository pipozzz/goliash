-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Where an image's package list came from: its SBOM attestation, or its package database read by Goliash.
ALTER TABLE image_sboms ADD COLUMN source TEXT NOT NULL DEFAULT 'sbom';

-- +goose Down
ALTER TABLE image_sboms DROP COLUMN source;
