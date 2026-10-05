-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The upstream repository a public registry refused to show anonymously: the agents
-- check it with their credentials while it is the service's upstream.
ALTER TABLE services ADD COLUMN private_upstream TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE services DROP COLUMN private_upstream;
