-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- The team a workload's labels name (goliash.team, team, owner).
ALTER TABLE instances ADD COLUMN team TEXT NOT NULL DEFAULT '';
-- Where a service's owner came from: '' set by people, 'label' from its workloads'
-- labels, 'app' from its application's team. Only the last two follow changes.
ALTER TABLE services ADD COLUMN owner_source TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE services DROP COLUMN owner_source;
ALTER TABLE instances DROP COLUMN team;
