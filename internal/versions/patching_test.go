// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Two teams' upgrades in production: the time to patch is from the first release after the old version to the
// deploy; a rollback and a staging deploy do not count.
func TestTeamPatching(t *testing.T) {
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
	release := func(svc store.Service, v string, published time.Time) {
		if _, err := l.st.InsertReleases(ctx, l.sc, svc.ID, []string{v}); err != nil {
			t.Fatal(err)
		}
		if err := l.st.SetReleaseInfo(ctx, l.sc, svc.ID, v, published, ""); err != nil {
			t.Fatal(err)
		}
	}
	release(api, "1.1.0", day(9, 1))
	release(api, "1.2.0", day(9, 20))
	release(db, "16.3", day(8, 1))
	release(db, "16.4", day(10, 1))
	prod, stg := l.targets["prod-a"].EnvironmentID, l.targets["stg-1"].EnvironmentID
	deploy := func(svc store.Service, env, from, to string, at time.Time) {
		if err := l.st.InsertEvent(ctx, l.sc, store.Event{ID: store.NewID(), Type: "version_changed", ServiceID: svc.ID, EnvironmentID: env, FromVersion: from, ToVersion: to, At: at}); err != nil {
			t.Fatal(err)
		}
	}
	deploy(api, prod, "1.0.0", "1.2.0", day(9, 11)) // behind since 1.1.0 (1 Sep): 10 days
	deploy(api, prod, "1.2.0", "1.1.0", day(9, 12)) // a rollback
	deploy(db, prod, "16.2", "16.4", day(10, 11))   // behind since 16.3 (1 Aug): 71 days
	deploy(db, stg, "16.2", "16.4", day(10, 2))     // staging
	deploy(api, prod, "1.0.0", "1.2.0", day(9, 13)) // the same version on another target: counted once
	o, err := LoadOverview(ctx, l.st, l.sc)
	if err != nil {
		t.Fatal(err)
	}
	samples, err := PatchTimes(ctx, l.st, l.sc, o, prod, now.AddDate(0, -6, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 || samples[0].Service != "api" || samples[0].Days != 10 || samples[1].Service != "db" || samples[1].Days != 71 {
		t.Fatalf("samples %+v", samples)
	}
	pos := Posture{Prod: store.Environment{ID: prod}}
	teams, _, err := LoadTeamPatching(ctx, l.st, l.sc, o, pos, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]TeamPatching{}
	for _, tp := range teams {
		got[tp.Owner] = tp
	}
	if d := got["team-data"]; d.Patched != 1 || d.MedianPatch != 71 || d.Monthly[PatchMonths-1] != 71 {
		t.Errorf("team-data %+v", d)
	}
	if p := got["team-pay"]; p.Patched != 1 || p.MedianPatch != 10 || p.Monthly[PatchMonths-2] != 10 || p.Monthly[PatchMonths-1] != -1 {
		t.Errorf("team-pay %+v", p)
	}
	months := PatchByMonth(samples, now)
	if last := months[PatchMonths-1]; last.Upgrades != 1 || last.Median != 71 || last.Month.Month() != time.October {
		t.Errorf("months %+v", months)
	}
}
