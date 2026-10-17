-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- A service's kind is worked out from its image unless someone chose it. Services were all created 'own' and
-- left so unless a person changed them: only third_party counts as chosen.
ALTER TABLE services ADD COLUMN kind_chosen BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE services SET kind_chosen = TRUE WHERE kind = 'third_party';

-- +goose Down
ALTER TABLE services DROP COLUMN kind_chosen;
