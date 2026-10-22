-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- What a release's notes say breaks: a line or two quoted from them, "" when they say nothing of it.
ALTER TABLE releases ADD COLUMN breaking TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE releases DROP COLUMN breaking;
