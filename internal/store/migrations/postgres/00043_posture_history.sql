-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The security posture of production, one row per workspace and day (the day's last reading): the trend
-- behind "is patching getting faster?".
CREATE TABLE posture_history (
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    day           TEXT NOT NULL, -- YYYY-MM-DD, UTC
    running       INTEGER NOT NULL,
    exposed       INTEGER NOT NULL,
    median_days   INTEGER NOT NULL,
    p90_days      INTEGER NOT NULL,
    behind_30     INTEGER NOT NULL,
    behind_90     INTEGER NOT NULL,
    eol_passed    INTEGER NOT NULL,
    eol_soon      INTEGER NOT NULL,
    accepted      INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, day)
);

-- +goose Down
DROP TABLE posture_history;
