// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// With approval asked for, a member's put-off needs a reason and waits; an admin approves it on the Security page.
func TestAcceptedRiskApproval(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	name := e.serviceOf("evil")
	admin, member := e.as(store.RoleAdmin), e.as(store.RoleMember)
	_, body, _ := post(t, admin, e.srv.URL+"/security/deadlines", url.Values{"exploited": {"0"}, "security": {"0"}, "eol": {"0"}, "approval": {"on"}})
	if rem, _ := e.st.Remediation(ctx, sc); !rem.Approval {
		t.Fatalf("approval not saved: %s", body[:min(len(body), 300)])
	}

	ack := e.srv.URL + "/services/" + url.PathEscape(name) + "/ack"
	_, body, _ = post(t, member, ack, url.Values{"kind": {"release"}, "for": {"720h"}})
	if !strings.Contains(body, "Say why the risk is accepted") {
		t.Fatal("a member's acknowledgement without a reason was taken")
	}
	_, body, _ = post(t, member, ack, url.Values{"kind": {"release"}, "for": {"720h"}, "reason": {"behind the VPN only"}})
	if !strings.Contains(body, "Sent for approval") {
		t.Fatalf("not sent for approval: %s", body[:min(len(body), 300)])
	}
	pending, _ := e.st.ListPendingAcks(ctx, sc)
	if acks, _ := e.st.ListAcks(ctx, sc); len(pending) != 1 || len(acks) != 0 {
		t.Fatalf("pending %+v acks %+v", pending, acks)
	}

	_, page := get(t, member, e.srv.URL+"/security", nil)
	if !strings.Contains(page, "behind the VPN only") || !strings.Contains(page, "waiting for approval") || strings.Contains(page, "/approve") {
		t.Error("a member sees the risk waiting, without approving it")
	}
	_, page = get(t, admin, e.srv.URL+"/security", nil)
	if !strings.Contains(page, "/security/risks/"+pending[0].ID+"/approve") || !strings.Contains(page, "1 to approve") {
		t.Fatal("an admin cannot approve it")
	}
	if code, _, _ := post(t, member, e.srv.URL+"/security/risks/"+pending[0].ID+"/approve", nil); code < 400 {
		t.Errorf("a member approved: %d", code)
	}
	_, body, _ = post(t, admin, e.srv.URL+"/security/risks/"+pending[0].ID+"/approve", nil)
	if !strings.Contains(body, "Approved") {
		t.Fatalf("approve: %s", body[:min(len(body), 300)])
	}
	acks, _ := e.st.ListAcks(ctx, sc)
	if len(acks) != 1 || acks[0].Reason != "behind the VPN only" || acks[0].ApprovedBy == "" {
		t.Fatalf("after approval %+v", acks)
	}

	// An admin's own acknowledgement needs neither.
	_, body, _ = post(t, admin, ack, url.Values{"kind": {"drift"}, "for": {"168h"}})
	if !strings.Contains(body, "Acknowledged.") {
		t.Errorf("admin: %s", body[:min(len(body), 300)])
	}
}
