-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Vulnerabilities people found do not affect a service (a VEX "not affected" statement): why, by whom, until when it
-- is reviewed. While in force, the vulnerability is left out of the service's findings, alerts and deadlines.
CREATE TABLE vuln_exceptions (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL,
    cve             TEXT NOT NULL,
    service_id      TEXT NOT NULL,
    justification   TEXT NOT NULL,
    note            TEXT NOT NULL,
    review_at       TIMESTAMP NOT NULL,
    created_by      TEXT NOT NULL,
    created_at      TIMESTAMP NOT NULL,
    closed_at       TIMESTAMP
);
CREATE INDEX vuln_exceptions_open_idx ON vuln_exceptions (workspace_id, cve);

-- +goose Down
DROP TABLE vuln_exceptions;
