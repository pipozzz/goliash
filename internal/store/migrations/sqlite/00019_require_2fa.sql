-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- An organization may require two-factor sign-in from everyone who signs in with a
-- password or a link.
ALTER TABLE organizations ADD COLUMN require_2fa INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE organizations DROP COLUMN require_2fa;
