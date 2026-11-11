// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// maxImageTags bounds the tags kept per repository: the newest pushed.
const maxImageTags = 500

// TagTime is a tag of a repository, when it was pushed, the image it names and when the repository's lifecycle
// policy deletes that image (zero: no rule does).
type TagTime struct {
	Tag       string
	PushedAt  time.Time
	Digest    string
	ExpiresAt time.Time
	Repo      string // set by ExpiringTags
}

// SetTagTimes replaces what a repository's tags were pushed when, keeping the newest maxImageTags.
func (s *Store) SetTagTimes(ctx context.Context, sc Scope, repo string, times map[string]time.Time) error {
	list := make([]TagTime, 0, len(times))
	for t, at := range times {
		list = append(list, TagTime{Tag: t, PushedAt: at.UTC()})
	}
	return s.SetTagFacts(ctx, sc, repo, list)
}

// SetTagFacts replaces what is known of a repository's tags, keeping the newest pushed maxImageTags.
func (s *Store) SetTagFacts(ctx context.Context, sc Scope, repo string, list []TagTime) error {
	sort.Slice(list, func(i, j int) bool { return list[i].PushedAt.After(list[j].PushedAt) })
	if len(list) > maxImageTags {
		list = list[:maxImageTags]
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `DELETE FROM image_tags WHERE org_id = ? AND workspace_id = ? AND repo = ?`,
			sc.OrgID, sc.WorkspaceID, repo); err != nil {
			return err
		}
		for _, t := range list {
			var expires sql.NullTime
			if !t.ExpiresAt.IsZero() {
				expires = sql.NullTime{Time: t.ExpiresAt.UTC(), Valid: true}
			}
			if _, err := s.exec(ctx, tx, `INSERT INTO image_tags (org_id, workspace_id, repo, tag, pushed_at, digest, expires_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, sc.OrgID, sc.WorkspaceID, repo, t.Tag, t.PushedAt.UTC(), t.Digest, expires); err != nil {
				return err
			}
		}
		return nil
	})
}

// TagTimes lists a repository's tags, newest pushed first.
func (s *Store) TagTimes(ctx context.Context, sc Scope, repo string) ([]TagTime, error) {
	rows, err := s.query(ctx, s.db, `SELECT tag, pushed_at, digest, expires_at, repo FROM image_tags WHERE org_id = ? AND workspace_id = ? AND repo = ?
		ORDER BY pushed_at DESC, tag`, sc.OrgID, sc.WorkspaceID, repo)
	if err != nil {
		return nil, err
	}
	return scanTagTimes(rows)
}

// ExpiringTags lists the tags whose image a lifecycle policy deletes before the given time, across repositories.
func (s *Store) ExpiringTags(ctx context.Context, sc Scope, before time.Time) ([]TagTime, error) {
	rows, err := s.query(ctx, s.db, `SELECT tag, pushed_at, digest, expires_at, repo FROM image_tags
		WHERE org_id = ? AND workspace_id = ? AND expires_at IS NOT NULL AND expires_at < ? ORDER BY expires_at, repo, tag`,
		sc.OrgID, sc.WorkspaceID, before.UTC())
	if err != nil {
		return nil, err
	}
	return scanTagTimes(rows)
}

func scanTagTimes(rows *sql.Rows) ([]TagTime, error) {
	defer func() { _ = rows.Close() }()
	var out []TagTime
	for rows.Next() {
		var t TagTime
		var expires sql.NullTime
		if err := rows.Scan(&t.Tag, &t.PushedAt, &t.Digest, &expires, &t.Repo); err != nil {
			return nil, err
		}
		t.PushedAt = t.PushedAt.UTC()
		if expires.Valid {
			t.ExpiresAt = expires.Time.UTC()
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
