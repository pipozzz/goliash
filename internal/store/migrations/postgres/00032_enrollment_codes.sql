-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Enrollment codes let agents register themselves into an environment. A code for one
-- agent (max_agents 1) belongs to the first agent that uses it; a code for many
-- (max_agents NULL) tells agents apart by identity. Expiry stops new agents only; an
-- agent that enrolled before can enroll again with the code until the code is revoked.
CREATE TABLE enrollment_codes (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    environment_id  TEXT NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    hash            TEXT NOT NULL UNIQUE,
    max_agents      INTEGER,
    created_by      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL,
    expires_at      TIMESTAMP,
    revoked_at      TIMESTAMP,
    last_used_at    TIMESTAMP
);
CREATE INDEX enrollment_codes_workspace_idx ON enrollment_codes (workspace_id);

-- What identifies an enrolled agent's installation (a Docker engine, a cluster), so
-- it gets its own agent back when it enrolls again.
ALTER TABLE agents ADD COLUMN identity TEXT;
ALTER TABLE agents ADD COLUMN enrollment_code_id TEXT;
CREATE UNIQUE INDEX agents_identity_idx ON agents (workspace_id, identity);

-- Targets an agent found or declared itself carry the key the agent names them by;
-- the agent owns their settings. NULL for targets set up on the server.
ALTER TABLE targets ADD COLUMN agent_key TEXT;

-- +goose Down
ALTER TABLE targets DROP COLUMN agent_key;
DROP INDEX agents_identity_idx;
ALTER TABLE agents DROP COLUMN enrollment_code_id;
ALTER TABLE agents DROP COLUMN identity;
DROP TABLE enrollment_codes;
