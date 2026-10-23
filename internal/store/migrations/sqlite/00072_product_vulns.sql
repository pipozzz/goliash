-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- What NVD says affects a version of a product (a CPE vendor:product), shared by every workspace like OSV's answers.
CREATE TABLE product_vulns (
    product     TEXT NOT NULL,
    version     TEXT NOT NULL,
    vulns       TEXT NOT NULL DEFAULT '[]',
    checked_at  TIMESTAMP NOT NULL,
    PRIMARY KEY (product, version)
);

-- +goose Down
DROP TABLE product_vulns;
