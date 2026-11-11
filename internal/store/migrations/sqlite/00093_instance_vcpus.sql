-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The vCPUs a managed database runs on, as its agent reads them (0 when not known): Extended Support is charged per vCPU.
ALTER TABLE instances ADD COLUMN vcpus INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE instances DROP COLUMN vcpus;
