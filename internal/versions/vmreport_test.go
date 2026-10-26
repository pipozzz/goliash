// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestVMReport(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 10, 0, 0, 0, time.UTC) }
	api, _ := l.st.EnsureService(ctx, l.sc, "api")
	api.Owner = "team-pay"
	_ = l.st.UpdateService(ctx, api)
	db, _ := l.st.EnsureService(ctx, l.sc, "db")
	db.Owner = "team-data"
	_ = l.st.UpdateService(ctx, db)
	release := func(svc store.Service, v string, published time.Time, security string) {
		if _, err := l.st.InsertReleases(ctx, l.sc, svc.ID, []string{v}); err != nil {
			t.Fatal(err)
		}
		if err := l.st.SetReleaseInfo(ctx, l.sc, svc.ID, v, published, ""); err != nil {
			t.Fatal(err)
		}
		if security != "" {
			if err := l.st.SetReleaseSecurity(ctx, l.sc, svc.ID, v, security); err != nil {
				t.Fatal(err)
			}
		}
	}
	release(api, "1.1.0", day(9, 1), "CVE-2026-1111")
	release(api, "1.2.0", day(9, 20), "")
	release(db, "16.3", day(8, 1), "security")
	release(db, "16.4", day(10, 1), "")
	prod := l.targets["prod-a"].EnvironmentID
	deploy := func(svc store.Service, from, to string, at time.Time) {
		if err := l.st.InsertEvent(ctx, l.sc, store.Event{ID: store.NewID(), Type: "version_changed", ServiceID: svc.ID, EnvironmentID: prod, FromVersion: from, ToVersion: to, At: at}); err != nil {
			t.Fatal(err)
		}
	}
	deploy(api, "1.0.0", "1.2.0", day(9, 11)) // 10 days, fixes CVE-2026-1111: in time
	deploy(db, "16.2", "16.4", day(9, 30))    // 60 days, a security fix: late
	deploy(db, "16.4", "16.5", day(10, 2))    // October: not in September's report
	if err := l.st.SetRemediation(ctx, l.sc, store.Remediation{Exploited: 7, Security: 30, EOL: 90}); err != nil {
		t.Fatal(err)
	}
	if err := l.st.RecordPosture(ctx, l.sc, store.PostureSample{Day: "2026-09-29", Running: 2, Exposed: 1}); err != nil {
		t.Fatal(err)
	}
	if err := l.st.RecordPosture(ctx, l.sc, store.PostureSample{Day: "2026-10-01", Running: 9}); err != nil {
		t.Fatal(err)
	}
	ack, err := l.st.CreateAck(ctx, store.Ack{Scope: l.sc, ServiceID: api.ID, Kind: "release", UntilVersion: "2.0.0", CreatedBy: "ciso"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = l.st.CreateAck(ctx, store.Ack{Scope: l.sc, ServiceID: db.ID, Kind: "release", UntilAt: now.Add(-time.Hour), CreatedBy: "x"}) // expired

	r, err := LoadVMReport(ctx, l.st, l.sc, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Prod.ID != prod || r.Policy.Security != 30 {
		t.Fatalf("prod %+v policy %+v", r.Prod, r.Policy)
	}
	if r.MonthEnd == nil || r.MonthEnd.Day != "2026-09-29" || r.MonthEnd.Exposed != 1 {
		t.Errorf("month end %+v", r.MonthEnd)
	}
	if len(r.Upgrades) != 2 || r.Upgrades[0].Service != "api" || r.Upgrades[0].SecurityFix != "CVE-2026-1111" || !*r.Upgrades[0].InTime ||
		r.Upgrades[1].SecurityFix != "security" || *r.Upgrades[1].InTime {
		t.Fatalf("upgrades %+v", r.Upgrades)
	}
	if r.Security != 2 || r.InTime != 1 || r.SLAPercent() != 50 || r.Median != 10 || r.P90 != 60 {
		t.Errorf("security %d in time %d sla %d median %d", r.Security, r.InTime, r.SLAPercent(), r.Median)
	}
	if len(r.Teams) != 2 || r.Teams[0].Owner != "team-data" || r.Teams[0].InTime != 0 || r.Teams[1].InTime != 1 {
		t.Errorf("teams %+v", r.Teams)
	}
	if len(r.Accepted) != 1 || r.Accepted[0].ID != ack.ID || r.Accepted[0].Service != "api" {
		t.Errorf("accepted %+v", r.Accepted)
	}

	r.Policy.Security = 0
	if r.SLAPercent() != -1 {
		t.Error("no deadline should have no SLA")
	}
}
