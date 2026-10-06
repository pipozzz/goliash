// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// Channel is where notifications go: Slack, Discord, Telegram, ntfy, a generic webhook or e-mail.
type Channel struct {
	ID        string
	Scope     Scope
	Type      string // slack, webhook or email
	Name      string
	Config    json.RawMessage // {"url": …}, {"url": …, "secret": …} or {"to": [...]}
	CreatedAt time.Time
}

// ChannelTypes are the notification channel types the server can send to.
var ChannelTypes = []string{"slack", "discord", "telegram", "ntfy", "webhook", "email", "grafana", "push"}

// CreateChannel adds a channel.
func (s *Store) CreateChannel(ctx context.Context, c Channel) (Channel, error) {
	if !slices.Contains(ChannelTypes, c.Type) {
		return Channel{}, fmt.Errorf("unknown channel type %q", c.Type)
	}
	c.ID, c.CreatedAt = NewID(), s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO notification_channels (id, org_id, workspace_id, type, name, config, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, c.ID, c.Scope.OrgID, c.Scope.WorkspaceID, c.Type, c.Name, s.seal(c.ID, string(c.Config)), c.CreatedAt)
	return c, err
}

// ListChannels returns the workspace's channels by name.
func (s *Store) ListChannels(ctx context.Context, sc Scope) ([]Channel, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, type, name, config, created_at FROM notification_channels
		WHERE org_id = ? AND workspace_id = ? ORDER BY name`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Channel
	for rows.Next() {
		c := Channel{Scope: sc}
		var cfg string
		if err := rows.Scan(&c.ID, &c.Type, &c.Name, &cfg, &c.CreatedAt); err != nil {
			return nil, err
		}
		if cfg, err = s.open(c.ID, cfg); err != nil {
			return nil, fmt.Errorf("channel %s: %w", c.Name, err)
		}
		c.Config, c.CreatedAt = json.RawMessage(cfg), c.CreatedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChannelByName returns a channel of the workspace.
func (s *Store) GetChannelByName(ctx context.Context, sc Scope, name string) (Channel, error) {
	chans, err := s.ListChannels(ctx, sc)
	if err != nil {
		return Channel{}, err
	}
	for _, c := range chans {
		if c.Name == name {
			return c, nil
		}
	}
	return Channel{}, ErrNotFound
}

// Rule routes events to a channel.
type Rule struct {
	ID         string
	Scope      Scope
	ChannelID  string
	EventTypes []string        // empty: every type
	Filter     json.RawMessage // services, owners, environments, min_jump, digest_hour
	Mode       string          // instant, daily or weekly
	Paused     bool            // a paused rule queues nothing
	CreatedAt  time.Time
}

// CreateRule adds a notification rule.
func (s *Store) CreateRule(ctx context.Context, r Rule) (Rule, error) {
	r.ID, r.CreatedAt = NewID(), s.now()
	if r.EventTypes == nil {
		r.EventTypes = []string{}
	}
	if len(r.Filter) == 0 {
		r.Filter = json.RawMessage(`{}`)
	}
	types, err := json.Marshal(r.EventTypes)
	if err != nil {
		return r, err
	}
	_, err = s.exec(ctx, s.db, `INSERT INTO notification_rules (id, org_id, workspace_id, channel_id, event_types, filter,
		mode, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, r.ID, r.Scope.OrgID, r.Scope.WorkspaceID, r.ChannelID,
		string(types), string(r.Filter), r.Mode, r.CreatedAt)
	return r, err
}

// ListRules returns the workspace's notification rules.
func (s *Store) ListRules(ctx context.Context, sc Scope) ([]Rule, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, channel_id, event_types, filter, mode, paused, created_at FROM notification_rules
		WHERE org_id = ? AND workspace_id = ? ORDER BY created_at, id`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Rule
	for rows.Next() {
		r := Rule{Scope: sc}
		var types, filter string
		if err := rows.Scan(&r.ID, &r.ChannelID, &types, &filter, &r.Mode, &r.Paused, &r.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(types), &r.EventTypes); err != nil {
			return nil, err
		}
		r.Filter, r.CreatedAt = json.RawMessage(filter), r.CreatedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// Ack quiets notifications for a service: until a version ("we know about 2.0, quiet
// until 2.1") and/or until a time ("quiet for 14 days").
type Ack struct {
	ID            string
	Scope         Scope
	ServiceID     string
	EnvironmentID string // empty: every environment
	Kind          string // release or drift
	UntilVersion  string
	UntilAt       time.Time
	CreatedBy     string
	CreatedAt     time.Time
}

// CreateAck records an acknowledgement.
func (s *Store) CreateAck(ctx context.Context, a Ack) (Ack, error) {
	a.ID, a.CreatedAt = NewID(), s.now()
	var until sql.NullTime
	if !a.UntilAt.IsZero() {
		until = sql.NullTime{Time: a.UntilAt.UTC(), Valid: true}
	}
	_, err := s.exec(ctx, s.db, `INSERT INTO acks (id, org_id, workspace_id, service_id, environment_id, kind, until_version,
		until_at, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, a.ID, a.Scope.OrgID, a.Scope.WorkspaceID,
		a.ServiceID, nullString(a.EnvironmentID), a.Kind, a.UntilVersion, until, a.CreatedBy, a.CreatedAt)
	return a, err
}

// ListAcks returns the acknowledgements of a workspace, newest first.
func (s *Store) ListAcks(ctx context.Context, sc Scope) ([]Ack, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, service_id, environment_id, kind, until_version, until_at, created_by,
		created_at FROM acks WHERE org_id = ? AND workspace_id = ? ORDER BY created_at DESC, id DESC`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Ack
	for rows.Next() {
		a := Ack{Scope: sc}
		var env sql.NullString
		var until sql.NullTime
		if err := rows.Scan(&a.ID, &a.ServiceID, &env, &a.Kind, &a.UntilVersion, &until, &a.CreatedBy, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.EnvironmentID, a.UntilAt, a.CreatedAt = env.String, timeOrZero(until), a.CreatedAt.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// QueueItem is one notification waiting for delivery.
type QueueItem struct {
	ID        string
	Scope     Scope
	RuleID    string
	Payload   json.RawMessage
	DedupKey  string
	DueAt     time.Time
	Attempts  int
	LastError string
}

// Enqueue adds a notification unless the rule already has one with the same dedup key.
// It reports whether the item was added.
func (s *Store) Enqueue(ctx context.Context, q QueueItem) (bool, error) {
	res, err := s.exec(ctx, s.db, `INSERT INTO notification_queue (id, org_id, workspace_id, rule_id, payload, dedup_key,
		due_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (rule_id, dedup_key) DO NOTHING`,
		NewID(), q.Scope.OrgID, q.Scope.WorkspaceID, q.RuleID, string(q.Payload), q.DedupKey, q.DueAt.UTC(), s.now())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DueItems returns unsent items due by now with fewer than maxAttempts attempts, across workspaces.
func (s *Store) DueItems(ctx context.Context, now time.Time, maxAttempts int) ([]QueueItem, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, org_id, workspace_id, rule_id, payload, dedup_key, due_at, attempts, last_error
		FROM notification_queue WHERE sent_at IS NULL AND due_at <= ? AND attempts < ? ORDER BY due_at, id LIMIT 500`,
		now.UTC(), maxAttempts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []QueueItem
	for rows.Next() {
		var q QueueItem
		var payload string
		if err := rows.Scan(&q.ID, &q.Scope.OrgID, &q.Scope.WorkspaceID, &q.RuleID, &payload, &q.DedupKey, &q.DueAt,
			&q.Attempts, &q.LastError); err != nil {
			return nil, err
		}
		q.Payload, q.DueAt = json.RawMessage(payload), q.DueAt.UTC()
		out = append(out, q)
	}
	return out, rows.Err()
}

// MarkSent records successful delivery of items.
func (s *Store) MarkSent(ctx context.Context, ids []string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		for _, id := range ids {
			if _, err := s.exec(ctx, tx, `UPDATE notification_queue SET sent_at = ?, attempts = attempts + 1, last_error = ''
				WHERE id = ?`, now, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// MarkFailed records a failed attempt and when to retry.
func (s *Store) MarkFailed(ctx context.Context, ids []string, msg string, retryAt time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := s.exec(ctx, tx, `UPDATE notification_queue SET attempts = attempts + 1, last_error = ?, due_at = ?
				WHERE id = ?`, msg, retryAt.UTC(), id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ActiveRules returns the rules that are not paused.
func (s *Store) ActiveRules(ctx context.Context, sc Scope) ([]Rule, error) {
	all, err := s.ListRules(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, r := range all {
		if !r.Paused {
			out = append(out, r)
		}
	}
	return out, nil
}
