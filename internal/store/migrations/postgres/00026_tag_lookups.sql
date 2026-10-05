-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- A moving tag on a private registry is resolved by an agent: candidates holds the
-- tags (JSON array) the agent is asked to compare with the digest; empty once answered.
ALTER TABLE tag_resolutions ADD COLUMN candidates TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tag_resolutions DROP COLUMN candidates;
