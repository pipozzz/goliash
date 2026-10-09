-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The versions that fix an advisory, per ecosystem and package ("Debian:12|openssl" -> ["3.0.15-1~deb12u1"]);
-- empty until the record is read again with them.
ALTER TABLE osv_vulns ADD COLUMN fixes TEXT NOT NULL DEFAULT '';

-- CISA's catalog of known exploited vulnerabilities: public data, shared by every workspace.
CREATE TABLE known_exploited (
    cve             TEXT PRIMARY KEY,
    vendor_product  TEXT NOT NULL DEFAULT '',
    name            TEXT NOT NULL DEFAULT '',
    date_added      TEXT NOT NULL DEFAULT '',
    due_date        TEXT NOT NULL DEFAULT '',
    ransomware      BOOLEAN NOT NULL DEFAULT FALSE,
    fetched_at      TIMESTAMP NOT NULL
);

-- +goose Down
DROP TABLE known_exploited;
ALTER TABLE osv_vulns DROP COLUMN fixes;
