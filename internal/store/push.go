// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PushSubscription is one browser subscribed to a web push channel.
type PushSubscription struct {
	ID        string
	ChannelID string
	UserID    string
	Endpoint  string
	Keys      string // JSON {"p256dh":…,"auth":…}
	Label     string // the browser, for the list of subscribed browsers
	CreatedAt time.Time
}

// SavePushSubscription stores a browser's subscription, replacing an earlier one of
// the same browser (endpoint) on the channel.
func (s *Store) SavePushSubscription(ctx context.Context, sc Scope, p PushSubscription) (PushSubscription, error) {
	p.ID, p.CreatedAt = NewID(), s.now()
	err := s.inTx(ctx, func(q *sql.Tx) error {
		if _, err := s.exec(ctx, q, `DELETE FROM push_subscriptions WHERE org_id = ? AND workspace_id = ? AND channel_id = ? AND endpoint = ?`,
			sc.OrgID, sc.WorkspaceID, p.ChannelID, p.Endpoint); err != nil {
			return err
		}
		_, err := s.exec(ctx, q, `INSERT INTO push_subscriptions (id, org_id, workspace_id, channel_id, user_id, endpoint, keys, label, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.ID, sc.OrgID, sc.WorkspaceID, p.ChannelID, p.UserID, p.Endpoint, s.seal(p.ID, p.Keys), p.Label, p.CreatedAt)
		return err
	})
	return p, err
}

// ListPushSubscriptions returns the browsers subscribed to a channel, oldest first.
func (s *Store) ListPushSubscriptions(ctx context.Context, sc Scope, channelID string) ([]PushSubscription, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, channel_id, user_id, endpoint, keys, label, created_at FROM push_subscriptions
		WHERE org_id = ? AND workspace_id = ? AND channel_id = ? ORDER BY created_at, id`, sc.OrgID, sc.WorkspaceID, channelID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PushSubscription
	for rows.Next() {
		var p PushSubscription
		if err := rows.Scan(&p.ID, &p.ChannelID, &p.UserID, &p.Endpoint, &p.Keys, &p.Label, &p.CreatedAt); err != nil {
			return nil, err
		}
		if p.Keys, err = s.open(p.ID, p.Keys); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePushSubscription forgets a browser on a channel. With a user ID, only that
// user's subscription goes (a person unsubscribing); without, any (the push service
// said the subscription is gone).
func (s *Store) DeletePushSubscription(ctx context.Context, sc Scope, channelID, endpoint, userID string) (bool, error) {
	q, args := `DELETE FROM push_subscriptions WHERE org_id = ? AND workspace_id = ? AND channel_id = ? AND endpoint = ?`,
		[]any{sc.OrgID, sc.WorkspaceID, channelID, endpoint}
	if userID != "" {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	res, err := s.exec(ctx, s.db, q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// PushCounts counts subscribed browsers per channel, and the user's own per channel.
func (s *Store) PushCounts(ctx context.Context, sc Scope, userID string) (all, mine map[string]int, err error) {
	rows, err := s.query(ctx, s.db, `SELECT channel_id, user_id FROM push_subscriptions WHERE org_id = ? AND workspace_id = ?`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	all, mine = map[string]int{}, map[string]int{}
	for rows.Next() {
		var ch, u string
		if err := rows.Scan(&ch, &u); err != nil {
			return nil, nil, err
		}
		all[ch]++
		if u == userID {
			mine[ch]++
		}
	}
	return all, mine, rows.Err()
}

// ServerSecret returns a server-wide secret, made by generate and stored the first
// time it is asked for. When two servers race, the first stored value wins.
func (s *Store) ServerSecret(ctx context.Context, name string, generate func() (string, error)) (string, error) {
	var stored string
	err := s.queryRow(ctx, s.db, `SELECT value FROM server_secrets WHERE name = ?`, name).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		v, gerr := generate()
		if gerr != nil {
			return "", gerr
		}
		if _, err := s.exec(ctx, s.db, `INSERT INTO server_secrets (name, value) VALUES (?, ?) ON CONFLICT (name) DO NOTHING`, name, s.seal("server:"+name, v)); err != nil {
			return "", err
		}
		err = s.queryRow(ctx, s.db, `SELECT value FROM server_secrets WHERE name = ?`, name).Scan(&stored)
	}
	if err != nil {
		return "", err
	}
	return s.open("server:"+name, stored)
}

// DeleteServerSecret forgets a server secret; the next use makes a new one.
func (s *Store) DeleteServerSecret(ctx context.Context, name string) error {
	_, err := s.exec(ctx, s.db, `DELETE FROM server_secrets WHERE name = ?`, name)
	return err
}
