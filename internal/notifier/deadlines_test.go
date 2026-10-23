// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// With remediation deadlines set, a team hears, once each, that one of its upgrades nears its deadline and that it
// passed; another team does not.
func TestDeadlineAlerts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.db.Owner = "team-data"
	if err := e.st.UpdateService(ctx, e.db); err != nil {
		t.Fatal(err)
	}
	data, pay := newSink(t), newSink(t)
	e.rule(e.channel("slack", "data", map[string]any{"url": data.srv.URL}), "instant", []string{EventDeadline}, Filter{Owners: []string{"team-data"}})
	e.rule(e.channel("slack", "pay", map[string]any{"url": pay.srv.URL}), "instant", []string{EventDeadline}, Filter{Owners: []string{"team-pay"}})
	// PostgreSQL 13 reached its end of life on 20 Oct; the workspace gives itself three days.
	if _, err := e.st.OpenDrift(ctx, store.Drift{
		Scope: e.sc, ServiceID: e.db.ID, EnvironmentID: e.prod.ID, Kind: "eol",
		Detail: json.RawMessage(`{"running":"13.4","eol":"2026-10-20"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetRemediation(ctx, e.sc, store.Remediation{EOL: 3}); err != nil {
		t.Fatal(err)
	}

	e.clock = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	for range 2 {
		if err := e.n.CheckDeadlines(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	if data.count() != 1 || !strings.Contains(data.last(), "postgres @ prod: the remediation deadline for the end of life is 23 Oct") {
		t.Fatalf("near deadline: %d %s", data.count(), data.last())
	}
	e.clock = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	for range 2 {
		_ = e.n.CheckDeadlines(ctx)
		_ = e.n.DeliverDue(ctx)
	}
	if data.count() != 2 || !strings.Contains(data.last(), "passed on 23 Oct") || !strings.Contains(data.last(), "Remediation deadline passed") {
		t.Fatalf("passed deadline: %d %s", data.count(), data.last())
	}
	if pay.count() != 0 {
		t.Errorf("another team heard of it: %s", pay.last())
	}
	// A month later it is no longer announced; the brief and the report name it.
	e.clock = time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	_ = e.n.CheckDeadlines(ctx)
	_ = e.n.DeliverDue(ctx)
	if data.count() != 2 {
		t.Errorf("announced again: %d", data.count())
	}
}
