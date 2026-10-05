-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Leadership among servers sharing a database: the holder renews expires_at; when it
-- stops (crash, freeze, lost network), another server takes the lease once it expires.
CREATE TABLE leases (
    name        TEXT PRIMARY KEY,
    holder      TEXT NOT NULL,
    expires_at  TIMESTAMP NOT NULL
);
-- Failed sign-ins, shared by every server (keys are hashed e-mails and addresses).
CREATE TABLE signin_failures (
    key  TEXT NOT NULL,
    at   TIMESTAMP NOT NULL
);
CREATE INDEX signin_failures_key_idx ON signin_failures (key, at);

-- +goose Down
DROP TABLE signin_failures;
DROP TABLE leases;
