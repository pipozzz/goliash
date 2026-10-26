// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// An accepted risk waiting for approval asks for it once; one approved says once, three days ahead, that it ends.
// A put-off without a reason is no accepted risk and ends quietly.
func TestRiskAlerts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sink := newSink(t)
	e.rule(e.channel("slack", "sec", map[string]any{"url": sink.srv.URL}), "instant", []string{EventDeadline}, Filter{})
	e.clock = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	until := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	a, err := e.st.CreateAck(ctx, store.Ack{
		Scope: e.sc, ServiceID: e.db.ID, EnvironmentID: e.prod.ID, Kind: "release", UntilAt: until,
		CreatedBy: "dev@example.com", Reason: "internal only", Pending: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateAck(ctx, store.Ack{Scope: e.sc, ServiceID: e.db.ID, Kind: "drift", UntilAt: until, CreatedBy: "x"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.n.CheckDeadlines(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	if sink.count() != 1 || !strings.Contains(sink.last(), "dev@example.com accepts the risk until 31 Oct and waits for an admin") ||
		!strings.Contains(sink.last(), "internal only") {
		t.Fatalf("approval: %d %s", sink.count(), sink.last())
	}
	if err := e.st.ApproveAck(ctx, e.sc, a.ID, "ciso@example.com"); err != nil {
		t.Fatal(err)
	}
	e.clock = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	for range 2 {
		_ = e.n.CheckDeadlines(ctx)
		_ = e.n.DeliverDue(ctx)
	}
	if sink.count() != 2 || !strings.Contains(sink.last(), "the accepted risk ends on 31 Oct") {
		t.Fatalf("ends: %d %s", sink.count(), sink.last())
	}
}
