// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"time"
)

// ReleaseImage is the image a release tag pointed at when Goliash read it, for a release that runs nowhere yet.
type ReleaseImage struct {
	Repo, Tag, Digest string
	ReadAt            time.Time
}

// SetReleaseImage records the digest a release tag points at.
func (s *Store) SetReleaseImage(ctx context.Context, sc Scope, r ReleaseImage) error {
	_, err := s.exec(ctx, s.db, `INSERT INTO release_images (org_id, workspace_id, repo, tag, digest, read_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, repo, tag) DO UPDATE SET digest = excluded.digest, read_at = excluded.read_at`,
		sc.OrgID, sc.WorkspaceID, r.Repo, r.Tag, r.Digest, s.now())
	return err
}

// ListReleaseImages returns the workspace's release images keyed by repo + ":" + tag.
func (s *Store) ListReleaseImages(ctx context.Context, sc Scope) (map[string]ReleaseImage, error) {
	rows, err := s.query(ctx, s.db, `SELECT repo, tag, digest, read_at FROM release_images WHERE org_id = ? AND workspace_id = ?`,
		sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]ReleaseImage{}
	for rows.Next() {
		var r ReleaseImage
		if err := rows.Scan(&r.Repo, &r.Tag, &r.Digest, &r.ReadAt); err != nil {
			return nil, err
		}
		r.ReadAt = r.ReadAt.UTC()
		out[r.Repo+":"+r.Tag] = r
	}
	return out, rows.Err()
}

// RequestReleaseImage asks for the image behind a tag to be read (someone checks it before it runs): a row
// without a digest until the server or an agent reads it. A tag already read is left as it is.
func (s *Store) RequestReleaseImage(ctx context.Context, sc Scope, repo, tag string) error {
	_, err := s.exec(ctx, s.db, `INSERT INTO release_images (org_id, workspace_id, repo, tag, digest, read_at) VALUES (?, ?, ?, ?, '', ?)
		ON CONFLICT (workspace_id, repo, tag) DO NOTHING`, sc.OrgID, sc.WorkspaceID, repo, tag, s.now())
	return err
}
