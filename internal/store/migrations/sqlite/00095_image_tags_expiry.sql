-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The image a tag names (its digest) and when the repository's lifecycle policy deletes it (Amazon ECR), as the
-- agent reads the policy: running images it will delete are a hygiene finding.
ALTER TABLE image_tags ADD COLUMN digest TEXT NOT NULL DEFAULT '';
ALTER TABLE image_tags ADD COLUMN expires_at TIMESTAMP;

-- +goose Down
ALTER TABLE image_tags DROP COLUMN expires_at;
ALTER TABLE image_tags DROP COLUMN digest;
