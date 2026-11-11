-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The release cycles of an upstream product (f5:nginx) and the newest release of each, as JSON: to say, beside a fix
-- upstream, which version to run.
CREATE TABLE product_releases (
    product     TEXT PRIMARY KEY,
    cycles      TEXT NOT NULL,
    checked_at  TIMESTAMP NOT NULL
);

-- +goose Down
DROP TABLE product_releases;
