// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// AuditEntry is one change to configuration, tokens or roles.
type AuditEntry struct {
	ID          string
	OrgID       string
	WorkspaceID string // empty for organization-wide changes
	Actor       string // e-mail, "api token" or "cli"
	Action      string // e.g. agent.create, user.role, policy.update
	Details     map[string]string
	At          time.Time
}

// Audit records an entry. Details must never contain secrets.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	if e.Details == nil {
		e.Details = map[string]string{}
	}
	details, err := json.Marshal(e.Details)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, s.db, `INSERT INTO audit_log (id, org_id, workspace_id, actor, action, details, at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, NewID(), e.OrgID, nullString(e.WorkspaceID), e.Actor, e.Action, string(details), s.now())
	return err
}

// ListAudit returns an organization's newest entries first.
func (s *Store) ListAudit(ctx context.Context, orgID string, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.query(ctx, s.db, `SELECT id, workspace_id, actor, action, details, at FROM audit_log
		WHERE org_id = ? ORDER BY at DESC, id DESC LIMIT ?`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AuditEntry
	for rows.Next() {
		e := AuditEntry{OrgID: orgID}
		var ws sql.NullString
		var details string
		if err := rows.Scan(&e.ID, &ws, &e.Actor, &e.Action, &details, &e.At); err != nil {
			return nil, err
		}
		e.WorkspaceID, e.At = ws.String, e.At.UTC()
		_ = json.Unmarshal([]byte(details), &e.Details)
		out = append(out, e)
	}
	return out, rows.Err()
}
