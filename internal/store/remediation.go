// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Remediation is how many days a workspace gives itself to upgrade away from each reason, counted from when the
// reason became known; 0 sets no deadline for it.
type Remediation struct {
	Exploited int `json:"exploited,omitempty"` // a vulnerability exploited in the wild runs (from CISA listing it)
	Security  int `json:"security,omitempty"`  // a newer release fixes vulnerabilities (from its discovery)
	EOL       int `json:"eol,omitempty"`       // the running release cycle reached its end of life (from that date)
	// Approval makes a member's accepted risk wait for an admin's approval, with a reason and an end.
	Approval bool `json:"approval,omitempty"`
}

// AcceptDays is the longest a risk accepted while waiting for approval may last: one made without an end, or
// until a version, ends then.
const AcceptDays = 90

// ErrNeedsReason is returned for an acknowledgement that needs approval but gives no reason.
var ErrNeedsReason = errors.New("say why the risk is accepted: an admin approves it")

// Accept applies the approval rule to an acknowledgement someone with role makes: with Approval set, one a
// member makes needs a reason, ends within AcceptDays, and waits for an admin.
func (r Remediation) Accept(a *Ack, role string, now time.Time) error {
	a.Reason = strings.TrimSpace(a.Reason)
	if !r.Approval || RoleRank(role) >= RoleRank(RoleAdmin) {
		return nil
	}
	if a.Reason == "" {
		return ErrNeedsReason
	}
	if limit := now.AddDate(0, 0, AcceptDays); a.UntilAt.IsZero() || a.UntilAt.After(limit) {
		a.UntilAt = limit
	}
	a.Pending = true
	return nil
}

// Any reports whether any deadline is set.
func (r Remediation) Any() bool { return r.Exploited > 0 || r.Security > 0 || r.EOL > 0 }

// Remediation returns the workspace's remediation deadlines.
func (s *Store) Remediation(ctx context.Context, sc Scope) (Remediation, error) {
	var raw string
	if err := s.queryRow(ctx, s.db, `SELECT remediation FROM workspaces WHERE org_id = ? AND id = ?`, sc.OrgID, sc.WorkspaceID).Scan(&raw); err != nil {
		return Remediation{}, err
	}
	var r Remediation
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &r)
	}
	return r, nil
}

// SetRemediation replaces the workspace's remediation deadlines.
func (s *Store) SetRemediation(ctx context.Context, sc Scope, r Remediation) error {
	raw := ""
	if r.Any() || r.Approval {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	_, err := s.exec(ctx, s.db, `UPDATE workspaces SET remediation = ? WHERE org_id = ? AND id = ?`, raw, sc.OrgID, sc.WorkspaceID)
	return err
}

// ClaimDefaultAlerts marks the workspace as given its default security alerts and reports whether this call did:
// true once per workspace, so a rule deleted afterwards is not created again.
func (s *Store) ClaimDefaultAlerts(ctx context.Context, sc Scope) (bool, error) {
	res, err := s.exec(ctx, s.db, `UPDATE workspaces SET default_alerts = 1 WHERE org_id = ? AND id = ? AND default_alerts = 0`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
