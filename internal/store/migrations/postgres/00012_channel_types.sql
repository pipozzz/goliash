-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- Channel types are validated by the server, so new types need no migration.

-- +goose Up
ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_type_check;

-- +goose Down
DELETE FROM notification_channels WHERE type NOT IN ('slack', 'webhook', 'email');
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_type_check
    CHECK (type IN ('slack', 'webhook', 'email'));
