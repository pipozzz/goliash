-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- A mapping rule can match a workload within one application ("db" in velin-lawrio).
ALTER TABLE mapping_rules DROP CONSTRAINT IF EXISTS mapping_rules_match_type_check;
ALTER TABLE mapping_rules ADD CONSTRAINT mapping_rules_match_type_check
    CHECK (match_type IN ('image_repo', 'workload_name', 'label', 'ignore', 'app_workload'));

-- +goose Down
DELETE FROM mapping_rules WHERE match_type = 'app_workload';
ALTER TABLE mapping_rules DROP CONSTRAINT IF EXISTS mapping_rules_match_type_check;
ALTER TABLE mapping_rules ADD CONSTRAINT mapping_rules_match_type_check
    CHECK (match_type IN ('image_repo', 'workload_name', 'label', 'ignore'));
