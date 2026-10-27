// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"time"
)

// Ticket is one filed in an issue tracker for an upgrade of a service in an environment (and application).
type Ticket struct {
	ID                       string
	Scope                    Scope
	ChannelID                string
	ServiceID, EnvironmentID string
	App                      string
	ExternalID, ExternalKey  string // the tracker's ID and key (PAY-123)
	URL                      string
	Title                    string
	LastNote                 string // the last thing said on it, so the same is not said twice
	CreatedAt                time.Time
	ClosedAt                 time.Time // zero while open
}

// Open reports whether the ticket is still open.
func (t Ticket) Open() bool { return t.ClosedAt.IsZero() }

// CreateTicket records a filed ticket.
func (s *Store) CreateTicket(ctx context.Context, t Ticket) (Ticket, error) {
	t.ID, t.CreatedAt = NewID(), s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO tickets (id, org_id, workspace_id, channel_id, service_id, environment_id, app,
		external_id, external_key, url, title, last_note, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Scope.OrgID, t.Scope.WorkspaceID, t.ChannelID, t.ServiceID, t.EnvironmentID, t.App, t.ExternalID, t.ExternalKey,
		t.URL, t.Title, t.LastNote, t.CreatedAt)
	return t, err
}

// OpenTicket returns the open ticket of a channel for an upgrade, ErrNotFound when there is none.
func (s *Store) OpenTicket(ctx context.Context, sc Scope, channelID, serviceID, envID, app string) (Ticket, error) {
	rows, err := s.listTickets(ctx, sc, `AND channel_id = ? AND service_id = ? AND environment_id = ? AND app = ? AND closed_at IS NULL`,
		channelID, serviceID, envID, app)
	if err != nil {
		return Ticket{}, err
	}
	if len(rows) == 0 {
		return Ticket{}, ErrNotFound
	}
	return rows[0], nil
}

// ListOpenTickets returns a workspace's open tickets, oldest first.
func (s *Store) ListOpenTickets(ctx context.Context, sc Scope) ([]Ticket, error) {
	return s.listTickets(ctx, sc, `AND closed_at IS NULL`)
}

// NoteTicket records what was last said on a ticket.
func (s *Store) NoteTicket(ctx context.Context, sc Scope, id, note string) error {
	res, err := s.exec(ctx, s.db, `UPDATE tickets SET last_note = ? WHERE workspace_id = ? AND id = ?`, note, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// CloseTicket records a ticket as closed.
func (s *Store) CloseTicket(ctx context.Context, sc Scope, id string) error {
	res, err := s.exec(ctx, s.db, `UPDATE tickets SET closed_at = ? WHERE workspace_id = ? AND id = ? AND closed_at IS NULL`,
		s.now(), sc.WorkspaceID, id)
	return expectOne(res, err)
}

func (s *Store) listTickets(ctx context.Context, sc Scope, where string, args ...any) ([]Ticket, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, channel_id, service_id, environment_id, app, external_id, external_key, url,
		title, last_note, created_at, closed_at FROM tickets WHERE org_id = ? AND workspace_id = ? `+where+` ORDER BY created_at, id`,
		append([]any{sc.OrgID, sc.WorkspaceID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Ticket
	for rows.Next() {
		t := Ticket{Scope: sc}
		var closed sql.NullTime
		if err := rows.Scan(&t.ID, &t.ChannelID, &t.ServiceID, &t.EnvironmentID, &t.App, &t.ExternalID, &t.ExternalKey, &t.URL,
			&t.Title, &t.LastNote, &t.CreatedAt, &closed); err != nil {
			return nil, err
		}
		t.CreatedAt, t.ClosedAt = t.CreatedAt.UTC(), timeOrZero(closed)
		out = append(out, t)
	}
	return out, rows.Err()
}
