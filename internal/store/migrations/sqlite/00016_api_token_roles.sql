-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- API tokens get a role (viewer or member; existing tokens acted as members) and an optional expiry.
ALTER TABLE tokens ADD COLUMN role TEXT NOT NULL DEFAULT 'member';
ALTER TABLE tokens ADD COLUMN expires_at TIMESTAMP;
ALTER TABLE tokens ADD COLUMN created_by TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tokens DROP COLUMN created_by;
ALTER TABLE tokens DROP COLUMN expires_at;
ALTER TABLE tokens DROP COLUMN role;
