-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- What "/" opens for a person: '' picks by device (tiles on phones, the table elsewhere), or table or tiles.
ALTER TABLE users ADD COLUMN home_view TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN home_view;
