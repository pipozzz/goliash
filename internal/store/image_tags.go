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

// TagTime is a tag of a repository and when it was pushed.
type TagTime struct {
	Tag      string
	PushedAt time.Time
}

// SetTagTimes replaces what a repository's tags were pushed when, keeping the newest maxImageTags.
func (s *Store) SetTagTimes(ctx context.Context, sc Scope, repo string, times map[string]time.Time) error {
	list := make([]TagTime, 0, len(times))
	for t, at := range times {
		list = append(list, TagTime{t, at.UTC()})
	}
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
			if _, err := s.exec(ctx, tx, `INSERT INTO image_tags (org_id, workspace_id, repo, tag, pushed_at) VALUES (?, ?, ?, ?, ?)`,
				sc.OrgID, sc.WorkspaceID, repo, t.Tag, t.PushedAt); err != nil {
				return err
			}
		}
		return nil
	})
}

// TagTimes lists a repository's tags, newest pushed first.
func (s *Store) TagTimes(ctx context.Context, sc Scope, repo string) ([]TagTime, error) {
	rows, err := s.query(ctx, s.db, `SELECT tag, pushed_at FROM image_tags WHERE org_id = ? AND workspace_id = ? AND repo = ?
		ORDER BY pushed_at DESC, tag`, sc.OrgID, sc.WorkspaceID, repo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TagTime
	for rows.Next() {
		var t TagTime
		if err := rows.Scan(&t.Tag, &t.PushedAt); err != nil {
			return nil, err
		}
		t.PushedAt = t.PushedAt.UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}
