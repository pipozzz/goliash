-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Who signed an image, as the certificates of its keyless signatures name them (JSON: identity and issuer).
-- NULL for images looked up before signers were read: signed ones are looked up once more.
ALTER TABLE image_evidence ADD COLUMN signers TEXT;

-- +goose Down
ALTER TABLE image_evidence DROP COLUMN signers;
