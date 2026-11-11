-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- What NVD says about a CVE's upstream products: the version ranges affected, per product (f5:nginx), as JSON. Read
-- when a distribution's advisory has no fix for a package, to say whether, and from which version, upstream fixed it.
CREATE TABLE upstream_cves (
    cve         TEXT PRIMARY KEY,
    ranges      TEXT NOT NULL,
    checked_at  TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE upstream_cves;
