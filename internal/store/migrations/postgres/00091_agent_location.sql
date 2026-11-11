-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Where an agent runs, as it reported when it registered (cloud, account, region, instance, os …): a JSON object.
ALTER TABLE agents ADD COLUMN location TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE agents DROP COLUMN location;
