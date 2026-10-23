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
	// BaseChain goes on down from BaseImage: what it is built on, and that on, each current or outdated. When a
	// link below is outdated, so is BaseStatus, though the image is built on BaseImage as it is now.
	BaseChain []BaseLink
	// Components is the image's dev.goliash.components label, as written: "nginx=1.27.2, php=8.3.12".
	Components  string
	Layers      []ImageLayer
	Signers     []ImageSigner // who signed it, from its keyless signatures' certificates
	SignersRead bool          // false for a lookup made before signers were read
}

// BaseLink is one step down a chain of base images: Image is built on Base, as Base is now (current) or an
// older build of it (outdated). How says where Goliash read the base: the image's label or its provenance.
type BaseLink struct {
	Image  string    `json:"image"`
	Base   string    `json:"base"`
	Status string    `json:"status"`
	Since  time.Time `json:"since,omitzero"` // when Base's current build was made, when known
	How    string    `json:"how,omitempty"`
}

// ImageSigner is who signed an image, as a keyless signature's certificate names it (not verified).
type ImageSigner struct {
	Identity string `json:"identity"`
	Issuer   string `json:"issuer,omitempty"`
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
	signers := []byte("[]")
	if len(ev.Signers) > 0 {
		if signers, err = json.Marshal(ev.Signers); err != nil {
			return err
		}
	}
	chain := ""
	if len(ev.BaseChain) > 0 {
		b, err := json.Marshal(ev.BaseChain)
		if err != nil {
			return err
		}
		chain = string(b)
	}
	_, err = s.exec(ctx, s.db, `INSERT INTO image_evidence (org_id, workspace_id, repo, digest, signed, sbom, provenance, found, error, checked_at, size_bytes,
		base_image, base_status, base_since, layers, signers, base_chain, components)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, repo, digest) DO UPDATE SET signed = excluded.signed, sbom = excluded.sbom,
			provenance = excluded.provenance, found = excluded.found, error = excluded.error, checked_at = excluded.checked_at,
			size_bytes = CASE WHEN excluded.size_bytes > 0 THEN excluded.size_bytes ELSE image_evidence.size_bytes END,
			base_image = excluded.base_image, base_status = excluded.base_status, base_since = excluded.base_since,
			layers = CASE WHEN excluded.layers <> '[]' THEN excluded.layers ELSE image_evidence.layers END,
			signers = excluded.signers, base_chain = excluded.base_chain, components = excluded.components`,
		sc.OrgID, sc.WorkspaceID, ev.Repo, ev.Digest, ev.Signed, ev.SBOM, ev.Provenance, string(found), ev.Error, s.now(), ev.Size,
		ev.BaseImage, ev.BaseStatus, nullTime(ev.BaseSince), string(layers), string(signers), chain, ev.Components)
	return err
}

// ListImageEvidence returns what is known of the workspace's images, keyed by repo + "@" + digest.
func (s *Store) ListImageEvidence(ctx context.Context, sc Scope) (map[string]ImageEvidence, error) {
	rows, err := s.query(ctx, s.db, `SELECT repo, digest, signed, sbom, provenance, found, error, checked_at, size_bytes, base_image, base_status, base_since, layers, signers, base_chain, components FROM image_evidence
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
		var signers sql.NullString
		var chain string
		if err := rows.Scan(&ev.Repo, &ev.Digest, &ev.Signed, &ev.SBOM, &ev.Provenance, &found, &ev.Error, &ev.CheckedAt, &ev.Size,
			&ev.BaseImage, &ev.BaseStatus, &since, &layers, &signers, &chain, &ev.Components); err != nil {
			return nil, err
		}
		ev.SignersRead = signers.Valid
		if chain != "" {
			_ = json.Unmarshal([]byte(chain), &ev.BaseChain)
		}
		_ = json.Unmarshal([]byte(signers.String), &ev.Signers)
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
