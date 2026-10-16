// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// ImageEvidence is what a registry holds beside a running image: signatures and attestations, found at
// CheckedAt. It records that they exist, not that they verify.
type ImageEvidence struct {
	Repo, Digest             string
	Signed, SBOM, Provenance bool
	Found                    []string
	Error                    string
	CheckedAt                time.Time
	Size                     int64     // bytes pulled: compressed layers and config; 0 when unknown
	BaseImage                string    // the base it was compared with, e.g. docker.io/library/node:22-alpine
	BaseStatus               string    // "" unknown, current, or outdated: built on an older build of the base
	BaseSince                time.Time // when the base's current build was made, when known
	Layers                   []ImageLayer
}

// ImageLayer is one layer of an image: compressed size and the build step that made it.
type ImageLayer struct {
	Digest  string `json:"digest"`
	Size    int64  `json:"size"`
	Command string `json:"command,omitempty"`
}

// SetImageEvidence records what was found for an image.
func (s *Store) SetImageEvidence(ctx context.Context, sc Scope, ev ImageEvidence) error {
	found, err := json.Marshal(ev.Found)
	if err != nil {
		return err
	}
	if ev.Found == nil {
		found = []byte("[]")
	}
	layers := []byte("[]")
	if len(ev.Layers) > 0 {
		if layers, err = json.Marshal(ev.Layers); err != nil {
			return err
		}
	}
	_, err = s.exec(ctx, s.db, `INSERT INTO image_evidence (org_id, workspace_id, repo, digest, signed, sbom, provenance, found, error, checked_at, size_bytes,
		base_image, base_status, base_since, layers)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, repo, digest) DO UPDATE SET signed = excluded.signed, sbom = excluded.sbom,
			provenance = excluded.provenance, found = excluded.found, error = excluded.error, checked_at = excluded.checked_at,
			size_bytes = CASE WHEN excluded.size_bytes > 0 THEN excluded.size_bytes ELSE image_evidence.size_bytes END,
			base_image = excluded.base_image, base_status = excluded.base_status, base_since = excluded.base_since,
			layers = CASE WHEN excluded.layers <> '[]' THEN excluded.layers ELSE image_evidence.layers END`,
		sc.OrgID, sc.WorkspaceID, ev.Repo, ev.Digest, ev.Signed, ev.SBOM, ev.Provenance, string(found), ev.Error, s.now(), ev.Size,
		ev.BaseImage, ev.BaseStatus, nullTime(ev.BaseSince), string(layers))
	return err
}

// ListImageEvidence returns what is known of the workspace's images, keyed by repo + "@" + digest.
func (s *Store) ListImageEvidence(ctx context.Context, sc Scope) (map[string]ImageEvidence, error) {
	rows, err := s.query(ctx, s.db, `SELECT repo, digest, signed, sbom, provenance, found, error, checked_at, size_bytes, base_image, base_status, base_since, layers FROM image_evidence
		WHERE org_id = ? AND workspace_id = ?`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]ImageEvidence{}
	for rows.Next() {
		var ev ImageEvidence
		var found string
		var since sql.NullTime
		var layers string
		if err := rows.Scan(&ev.Repo, &ev.Digest, &ev.Signed, &ev.SBOM, &ev.Provenance, &found, &ev.Error, &ev.CheckedAt, &ev.Size,
			&ev.BaseImage, &ev.BaseStatus, &since, &layers); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(layers), &ev.Layers)
		ev.BaseSince = since.Time.UTC()
		if !since.Valid {
			ev.BaseSince = time.Time{}
		}
		_ = json.Unmarshal([]byte(found), &ev.Found)
		ev.CheckedAt = ev.CheckedAt.UTC()
		out[ev.Repo+"@"+ev.Digest] = ev
	}
	return out, rows.Err()
}

// nullTime stores a zero time as NULL.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}
