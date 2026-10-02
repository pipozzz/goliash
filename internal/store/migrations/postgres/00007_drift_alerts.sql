-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- When a drift was announced (drift_detected). Drift shows in the UI at once but is
-- announced only after it has lasted the alert delay of its kind.
ALTER TABLE drifts ADD COLUMN notified_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE drifts DROP COLUMN notified_at;
