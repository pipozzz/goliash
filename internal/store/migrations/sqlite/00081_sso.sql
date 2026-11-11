-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Single sign-on set up in Goliash rather than in the environment: one OpenID Connect provider per organization,
-- and the roles its groups give. The client secret is sealed with the secret key.
CREATE TABLE sso_settings (
    org_id        TEXT PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    issuer        TEXT NOT NULL,
    client_id     TEXT NOT NULL,
    client_secret TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    domains       TEXT NOT NULL DEFAULT '',
    groups_claim  TEXT NOT NULL DEFAULT '',
    scopes        TEXT NOT NULL DEFAULT '',
    group_roles   TEXT NOT NULL DEFAULT '',
    updated_by    TEXT NOT NULL DEFAULT '',
    updated_at    TIMESTAMP NOT NULL
);

-- +goose Down
DROP TABLE sso_settings;
