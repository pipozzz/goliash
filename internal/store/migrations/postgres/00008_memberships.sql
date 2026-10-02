-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Workspace access for members and viewers. Organization owners and admins reach
-- every workspace without a membership; everyone else only the workspaces listed here,
-- so an MSP can let a client into their own workspace only.
CREATE TABLE memberships (
    user_id       TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    role          TEXT NOT NULL CHECK (role IN ('admin', 'member', 'viewer')),
    created_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, workspace_id)
);
CREATE INDEX memberships_workspace_idx ON memberships (workspace_id);

-- Existing members and viewers keep the access they had: every workspace of their organization.
INSERT INTO memberships (user_id, workspace_id, role, created_at)
SELECT u.id, w.id, u.role, u.created_at
FROM users u JOIN workspaces w ON w.org_id = u.org_id
WHERE u.role IN ('member', 'viewer');

-- +goose Down
DROP TABLE memberships;
