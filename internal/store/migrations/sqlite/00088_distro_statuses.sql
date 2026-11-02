-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- What a distribution's security tracker says of a CVE in one of its source packages, when OSV may say otherwise:
-- not_affected (the release never had the vulnerable code) or undetermined (not assessed yet).
CREATE TABLE distro_statuses (
    ecosystem  TEXT NOT NULL, -- Debian:12
    package    TEXT NOT NULL, -- the source package
    cve        TEXT NOT NULL,
    status     TEXT NOT NULL,
    fetched_at TIMESTAMP NOT NULL,
    PRIMARY KEY (ecosystem, package, cve)
);

-- +goose Down
DROP TABLE distro_statuses;
