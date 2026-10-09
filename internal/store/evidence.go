// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
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
	_, err = s.exec(ctx, s.db, `INSERT INTO image_evidence (org_id, workspace_id, repo, digest, signed, sbom, provenance, found, error, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, repo, digest) DO UPDATE SET signed = excluded.signed, sbom = excluded.sbom,
			provenance = excluded.provenance, found = excluded.found, error = excluded.error, checked_at = excluded.checked_at`,
		sc.OrgID, sc.WorkspaceID, ev.Repo, ev.Digest, ev.Signed, ev.SBOM, ev.Provenance, string(found), ev.Error, s.now())
	return err
}

// ListImageEvidence returns what is known of the workspace's images, keyed by repo + "@" + digest.
func (s *Store) ListImageEvidence(ctx context.Context, sc Scope) (map[string]ImageEvidence, error) {
	rows, err := s.query(ctx, s.db, `SELECT repo, digest, signed, sbom, provenance, found, error, checked_at FROM image_evidence
		WHERE org_id = ? AND workspace_id = ?`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]ImageEvidence{}
	for rows.Next() {
		var ev ImageEvidence
		var found string
		if err := rows.Scan(&ev.Repo, &ev.Digest, &ev.Signed, &ev.SBOM, &ev.Provenance, &found, &ev.Error, &ev.CheckedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(found), &ev.Found)
		ev.CheckedAt = ev.CheckedAt.UTC()
		out[ev.Repo+"@"+ev.Digest] = ev
	}
	return out, rows.Err()
}
