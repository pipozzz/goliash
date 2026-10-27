-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- SCIM provisioning: a person the identity provider deactivated can no longer sign in, and the groups it pushes
-- decide roles as GOLIASH_GROUP_ROLES maps them.
ALTER TABLE users ADD COLUMN disabled_at TIMESTAMP;
ALTER TABLE users ADD COLUMN external_id TEXT NOT NULL DEFAULT '';

CREATE TABLE scim_groups (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    display_name  TEXT NOT NULL,
    external_id   TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL
);
CREATE INDEX scim_groups_org_idx ON scim_groups (org_id, display_name);

CREATE TABLE scim_group_members (
    group_id  TEXT NOT NULL REFERENCES scim_groups (id) ON DELETE CASCADE,
    user_id   TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX scim_group_members_user_idx ON scim_group_members (user_id);

-- +goose Down
DROP TABLE scim_group_members;
DROP TABLE scim_groups;
ALTER TABLE users DROP COLUMN external_id;
ALTER TABLE users DROP COLUMN disabled_at;
