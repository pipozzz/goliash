// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"time"
)

// VEXStatement is what an image's publisher says of one vulnerability in it, from an OpenVEX document.
type VEXStatement struct {
	Vulnerability string   `json:"vulnerability"`
	Status        string   `json:"status"` // not_affected, fixed, affected or under_investigation
	Justification string   `json:"justification,omitempty"`
	Impact        string   `json:"impact,omitempty"`
	Packages      []string `json:"packages,omitempty"` // purls it is about; empty for the whole image
	Author        string   `json:"author,omitempty"`
}

// ImageVEX is the VEX statements read for an image, or why they could not be read.
type ImageVEX struct {
	Repo, Digest string
	Statements   []VEXStatement
	Error        string
	FetchedAt    time.Time
}

// SetImageVEX records an image's VEX statements.
func (s *Store) SetImageVEX(ctx context.Context, sc Scope, v ImageVEX) error {
	if v.Statements == nil {
		v.Statements = []VEXStatement{}
	}
	b, err := json.Marshal(v.Statements)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, s.db, `INSERT INTO image_vex (org_id, workspace_id, repo, digest, statements, error, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, repo, digest) DO UPDATE SET statements = excluded.statements, error = excluded.error,
			fetched_at = excluded.fetched_at`,
		sc.OrgID, sc.WorkspaceID, v.Repo, v.Digest, string(b), v.Error, s.now())
	return err
}

// ListImageVEX returns the workspace's VEX statements keyed by repo + "@" + digest.
func (s *Store) ListImageVEX(ctx context.Context, sc Scope) (map[string]ImageVEX, error) {
	rows, err := s.query(ctx, s.db, `SELECT repo, digest, statements, error, fetched_at FROM image_vex
		WHERE org_id = ? AND workspace_id = ?`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]ImageVEX{}
	for rows.Next() {
		var v ImageVEX
		var raw string
		if err := rows.Scan(&v.Repo, &v.Digest, &raw, &v.Error, &v.FetchedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &v.Statements)
		v.FetchedAt = v.FetchedAt.UTC()
		out[v.Repo+"@"+v.Digest] = v
	}
	return out, rows.Err()
}
