// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcceptedRiskApproval(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	sc := ws.Scope()
	svc, _ := st.EnsureService(ctx, sc, "api")
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// Without approval asked for, anyone's acknowledgement is in force at once, approved by whoever made it.
	a := Ack{Scope: sc, ServiceID: svc.ID, Kind: "release", UntilVersion: "2.0", CreatedBy: "dev"}
	if err := (Remediation{}).Accept(&a, RoleMember, now); err != nil || a.Pending {
		t.Fatalf("no approval: %v %+v", err, a)
	}
	if a, err = st.CreateAck(ctx, a); err != nil || a.ApprovedBy != "dev" {
		t.Fatalf("created %v %+v", err, a)
	}

	// With it, a member must say why, and waits; the end is at most AcceptDays away. An admin does not wait.
	rem := Remediation{Approval: true}
	b := Ack{Scope: sc, ServiceID: svc.ID, Kind: "release", UntilVersion: "3.0", CreatedBy: "dev"}
	if err := rem.Accept(&b, RoleMember, now); !errors.Is(err, ErrNeedsReason) {
		t.Fatalf("no reason: %v", err)
	}
	b.Reason = " not exposed "
	if err := rem.Accept(&b, RoleMember, now); err != nil || !b.Pending || b.Reason != "not exposed" || !b.UntilAt.Equal(now.AddDate(0, 0, AcceptDays)) {
		t.Fatalf("member: %v %+v", err, b)
	}
	c := Ack{Kind: "release", UntilVersion: "3.0"}
	if err := rem.Accept(&c, RoleAdmin, now); err != nil || c.Pending {
		t.Fatalf("admin: %v %+v", err, c)
	}
	if b, err = st.CreateAck(ctx, b); err != nil {
		t.Fatal(err)
	}
	approved, _ := st.ListAcks(ctx, sc)
	pending, _ := st.ListPendingAcks(ctx, sc)
	if len(approved) != 1 || len(pending) != 1 || !pending[0].Pending || pending[0].Reason != "not exposed" {
		t.Fatalf("approved %+v pending %+v", approved, pending)
	}
	if err := st.ApproveAck(ctx, sc, b.ID, "ciso"); err != nil {
		t.Fatal(err)
	}
	if err := st.ApproveAck(ctx, sc, b.ID, "ciso"); !errors.Is(err, ErrNotFound) {
		t.Errorf("approved twice: %v", err)
	}
	approved, _ = st.ListAcks(ctx, sc)
	if len(approved) != 2 || approved[0].ApprovedBy != "ciso" || approved[0].Pending || approved[0].ApprovedAt.IsZero() {
		t.Errorf("after approval %+v", approved)
	}

	// Remediation keeps the approval setting with no deadlines set.
	if err := st.SetRemediation(ctx, sc, rem); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Remediation(ctx, sc); !got.Approval || got.Any() {
		t.Errorf("remediation %+v", got)
	}
}
