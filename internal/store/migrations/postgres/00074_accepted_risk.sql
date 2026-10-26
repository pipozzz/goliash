-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- An acknowledgement's reason, and who approved it: one a member made waits for an admin when the workspace asks
-- for approval. Those made before approval existed count as approved by whoever made them.
ALTER TABLE acks ADD COLUMN reason TEXT NOT NULL DEFAULT '';
ALTER TABLE acks ADD COLUMN approved_by TEXT NOT NULL DEFAULT '';
ALTER TABLE acks ADD COLUMN approved_at TIMESTAMPTZ;
UPDATE acks SET approved_by = created_by, approved_at = created_at;

-- +goose Down
ALTER TABLE acks DROP COLUMN approved_at;
ALTER TABLE acks DROP COLUMN approved_by;
ALTER TABLE acks DROP COLUMN reason;
