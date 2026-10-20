-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- A change freeze that comes back every week, the same days and hours ("no deploys on Friday afternoon").
ALTER TABLE change_freezes ADD COLUMN weekly BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE change_freezes DROP COLUMN weekly;
