-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- How a workload can be reached from outside its cluster or host, one way per line, as its agent reads it.
ALTER TABLE instances ADD COLUMN exposed TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE instances DROP COLUMN exposed;
