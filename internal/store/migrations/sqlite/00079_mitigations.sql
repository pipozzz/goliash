-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Mitigations of vulnerabilities exploited in the wild that have no fix yet: what was done instead of an upgrade,
-- for a service in an environment, until a fix is out or the vulnerable image no longer runs.
CREATE TABLE mitigations (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL,
    cve             TEXT NOT NULL,
    service_id      TEXT NOT NULL,
    environment_id  TEXT NOT NULL,
    kind            TEXT NOT NULL,
    measure         TEXT NOT NULL,
    review_at       TIMESTAMP NOT NULL,
    created_by      TEXT NOT NULL,
    created_at      TIMESTAMP NOT NULL,
    closed_at       TIMESTAMP,
    closed_reason   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX mitigations_open_idx ON mitigations (workspace_id, cve);

-- +goose Down
DROP TABLE mitigations;
