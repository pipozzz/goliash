-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- What security fixes a release names (CVE, GHSA or GO identifiers, or "security"), from its notes or its project.
ALTER TABLE releases ADD COLUMN security TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE releases DROP COLUMN security;
