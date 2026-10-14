// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// FeedMessage is one notification as people get it: the items one rule sent at once. The same message sent to
// several channels counts once.
type FeedMessage struct {
	Key    string // stable across channels: what the message holds
	SentAt time.Time
	Mode   string // instant, daily or weekly
	Items  []json.RawMessage
}

// NotificationFeed returns the messages a workspace's rules sent since since, newest first, at most limit.
func (s *Store) NotificationFeed(ctx context.Context, sc Scope, since time.Time, limit int) ([]FeedMessage, error) {
	rows, err := s.query(ctx, s.db, `SELECT q.rule_id, r.mode, q.payload, q.sent_at
		FROM notification_queue q JOIN notification_rules r ON r.id = q.rule_id
		WHERE q.workspace_id = ? AND q.sent_at IS NOT NULL AND q.sent_at > ?
		ORDER BY q.sent_at DESC, q.id LIMIT 5000`, sc.WorkspaceID, since.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type key struct {
		rule string
		at   time.Time
	}
	byMsg := map[key]*FeedMessage{}
	var order []key
	for rows.Next() {
		var rule, mode, payload string
		var at time.Time
		if err := rows.Scan(&rule, &mode, &payload, &at); err != nil {
			return nil, err
		}
		k := key{rule, at.UTC().Truncate(time.Millisecond)}
		m, ok := byMsg[k]
		if !ok {
			m = &FeedMessage{SentAt: at.UTC(), Mode: mode}
			byMsg[k] = m
			order = append(order, k)
		}
		m.Items = append(m.Items, json.RawMessage(payload))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []FeedMessage
	for _, k := range order {
		m := byMsg[k]
		texts := make([]string, len(m.Items))
		for i, it := range m.Items {
			texts[i] = string(it)
		}
		sort.Strings(texts)
		sum := sha256.Sum256([]byte(strings.Join(texts, "\n")))
		m.Key = hex.EncodeToString(sum[:8])
		if seen[m.Key] {
			continue // the same message through another channel
		}
		seen[m.Key] = true
		out = append(out, *m)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// NotificationsSeen is when the person last looked at their notifications in the workspace; zero if never.
func (s *Store) NotificationsSeen(ctx context.Context, sc Scope, userID string) (time.Time, error) {
	var at time.Time
	err := s.queryRow(ctx, s.db, `SELECT seen_at FROM notification_reads WHERE user_id = ? AND workspace_id = ?`,
		userID, sc.WorkspaceID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	return at.UTC(), err
}

// SeeNotifications records that the person looked at their notifications now.
func (s *Store) SeeNotifications(ctx context.Context, sc Scope, userID string) error {
	_, err := s.exec(ctx, s.db, `INSERT INTO notification_reads (user_id, workspace_id, seen_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id, workspace_id) DO UPDATE SET seen_at = excluded.seen_at`, userID, sc.WorkspaceID, s.now())
	return err
}
