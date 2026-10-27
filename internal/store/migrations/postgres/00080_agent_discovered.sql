-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- What an agent with a token found it could collect when it last started, so Goliash can offer to add it.
ALTER TABLE agents ADD COLUMN discovered TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE agents DROP COLUMN discovered;
