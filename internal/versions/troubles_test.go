// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A release rolled back in one environment is high risk for every upgrade to it, with where and when; a rollout
// that flapped back within minutes is no trouble.
func TestTroubledReleases(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	l.run(map[string]string{"stg-1": "1.27.2", "prod-a": "1.27.2"})
	now := time.Now()
	prod, stg := l.envs["prod"], l.envs["staging"]
	for _, e := range []store.Event{
		{Type: "version_changed", ServiceID: l.svc.ID, EnvironmentID: prod.ID, FromVersion: "1.28.0", ToVersion: "1.27.2", At: now.Add(-48 * time.Hour)},
		// staging: 1.27.3 went back and forward within minutes, a rolling update flapping
		{Type: "version_changed", ServiceID: l.svc.ID, EnvironmentID: stg.ID, FromVersion: "1.27.3", ToVersion: "1.27.2", At: now.Add(-3 * time.Hour)},
		{Type: "version_changed", ServiceID: l.svc.ID, EnvironmentID: stg.ID, FromVersion: "1.27.2", ToVersion: "1.27.3", At: now.Add(-3*time.Hour + 5*time.Minute)},
	} {
		if err := l.st.InsertEvent(ctx, l.sc, e); err != nil {
			t.Fatal(err)
		}
	}
	o, err := LoadOverview(ctx, l.st, l.sc)
	if err != nil {
		t.Fatal(err)
	}
	troubles, err := Troubles(ctx, l.st, l.sc, o, now)
	if err != nil {
		t.Fatal(err)
	}
	repo := o.Refs[l.svc.ID].Repo
	if got := troubles[repo+"|1.28.0"]; !strings.HasPrefix(got, "rolled back in prod (web) on ") {
		t.Fatalf("troubles %v (repo %q)", troubles, repo)
	}
	if _, flap := troubles[repo+"|1.27.3"]; flap {
		t.Fatal("a flapping rollout counted as trouble")
	}
	us := WithTroubles([]Update{
		{Service: l.svc, Environment: stg, Running: "1.27.2", Target: "1.28.0", Risk: RiskLow},
		{Service: l.svc, Environment: stg, Running: "1.27.2", Target: "1.27.3", Risk: RiskLow},
	}, o, troubles)
	if us[0].Risk != RiskHigh || !strings.HasPrefix(us[0].Trouble, "1.28.0 rolled back in prod") || us[1].Risk != RiskLow {
		t.Fatalf("updates %+v", us)
	}
}
