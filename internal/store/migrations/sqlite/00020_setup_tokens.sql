-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- While nobody has an account, the server logs a one-time link (hashed here) that
-- creates the first owner in the browser.
CREATE TABLE setup_tokens (
    id          TEXT PRIMARY KEY,
    expires_at  TIMESTAMP NOT NULL,
    used_at     TIMESTAMP,
    created_at  TIMESTAMP NOT NULL
);

-- +goose Down
DROP TABLE setup_tokens;
