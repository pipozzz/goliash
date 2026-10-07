-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- A mapping rule can match a workload within one application ("db" in velin-lawrio),
-- so same-named workloads of different projects map to different services. SQLite
-- cannot change a CHECK constraint, so the table is rebuilt with foreign keys off.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
-- +goose StatementBegin
BEGIN;
CREATE TABLE mapping_rules_new (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    priority        INTEGER NOT NULL DEFAULT 0,
    match_type      TEXT NOT NULL CHECK (match_type IN ('image_repo', 'workload_name', 'label', 'ignore', 'app_workload')),
    pattern         TEXT NOT NULL,
    service_id      TEXT REFERENCES services (id) ON DELETE CASCADE,
    created_at      TIMESTAMP NOT NULL
);
INSERT INTO mapping_rules_new (id, org_id, workspace_id, priority, match_type, pattern, service_id, created_at)
SELECT id, org_id, workspace_id, priority, match_type, pattern, service_id, created_at FROM mapping_rules;
DROP TABLE mapping_rules;
ALTER TABLE mapping_rules_new RENAME TO mapping_rules;
CREATE INDEX mapping_rules_workspace_idx ON mapping_rules (workspace_id, priority);
COMMIT;
-- +goose StatementEnd
PRAGMA foreign_keys = ON;

-- +goose Down
DELETE FROM mapping_rules WHERE match_type = 'app_workload';
