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

// Renaming or merging applications moves open drift to the new names, keeping its
// start: nothing is announced again.
func TestRenamedAppsKeepTheirDrift(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	// web runs in two applications in prod (a split row), behind upstream in both.
	for name, app := range map[string]string{"prod-a": "shop", "prod-b": "shop-billing"} {
		target := l.targets[name]
		if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: target.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
			TargetID: target.ID, EnvironmentID: target.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "web-" + app,
			WorkloadKind: "deployment", WorkloadName: "web", ContainerName: "nginx", Image: "nginx:1.27.2", Tag: "1.27.2",
			Running: 1, IsMain: true, App: app, AppSource: "app.kubernetes.io/part-of",
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.checker.CheckUpstreams(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	if err := l.checker.EvaluateDrift(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	apps := func() string {
		open, _ := l.st.OpenDrifts(ctx, l.sc)
		var out []string
		for _, d := range open {
			if d.Kind == "upstream" {
				out = append(out, d.App)
			}
		}
		return strings.Join(out, ",")
	}
	if got := apps(); got != "shop,shop-billing" && got != "shop-billing,shop" {
		t.Fatalf("upstream drift per application: %q", got)
	}
	detected := l.events("drift_detected")
	resolved := l.events("drift_resolved")

	// Rename: the drift follows the name.
	if err := l.st.RenameApp(ctx, l.sc, "shop-billing", "billing"); err != nil {
		t.Fatal(err)
	}
	_ = l.checker.EvaluateDrift(ctx, l.sc)
	if got := apps(); got != "shop,billing" && got != "billing,shop" {
		t.Fatalf("after rename: %q", got)
	}
	// Merge into shop: one drift for the whole service, the other resolved.
	if err := l.st.RenameApp(ctx, l.sc, "billing", "shop"); err != nil {
		t.Fatal(err)
	}
	_ = l.checker.EvaluateDrift(ctx, l.sc)
	if got := apps(); strings.Count(got, ",") != 0 {
		t.Fatalf("after merge, one upstream drift expected: %q", got)
	}
	if l.events("drift_detected") != detected || l.events("drift_resolved") != resolved {
		t.Fatalf("renames announced drift again or resolved it:\n%s %s\nwas\n%s %s", l.events("drift_detected"), l.events("drift_resolved"), detected, resolved)
	}
	names, _ := l.st.AppNames(ctx, l.sc)
	if names["shop-billing"] != "shop" || names["billing"] != "shop" {
		t.Fatalf("names %v", names)
	}
	// Split it out again, by the name its labels give.
	_ = l.st.RenameApp(ctx, l.sc, "shop-billing", "")
	if names, _ = l.st.AppNames(ctx, l.sc); names["shop-billing"] != "" {
		t.Fatalf("names after undo %v", names)
	}
	_ = l.checker.EvaluateDrift(ctx, l.sc)
	if got := apps(); got != "shop,shop-billing" && got != "shop-billing,shop" {
		t.Fatalf("after splitting out: %q", got)
	}
}

// An application's team goes to services in it without an owner, also ones that
// appear later; it follows renames; services whose applications disagree keep none.
func TestFillOwners(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	run := func(svc store.Service, target, app string) {
		tg := l.targets[target]
		existing, _ := l.st.ListTargetInstances(ctx, l.sc, tg.ID)
		ch := store.SnapshotChanges{Scope: l.sc, TargetID: tg.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: append(existing, store.Instance{
			TargetID: tg.ID, EnvironmentID: tg.EnvironmentID, ServiceID: svc.ID, WorkloadID: svc.Name + "-" + app,
			WorkloadKind: "deployment", WorkloadName: svc.Name, ContainerName: "app", Image: "nginx:1.27.2", Tag: "1.27.2",
			Running: 1, IsMain: true, App: app, AppSource: "app.kubernetes.io/part-of",
		})}
		if err := l.st.ApplySnapshot(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	run(l.svc, "prod-a", "shop-frontend")
	if err := l.st.RenameApp(ctx, l.sc, "shop-frontend", "webshop"); err != nil {
		t.Fatal(err)
	}
	if err := l.st.SetAppTeam(ctx, l.sc, "webshop", "team-shop"); err != nil {
		t.Fatal(err)
	}
	// Renaming the application (as shown) takes its team along.
	if err := l.st.RenameApp(ctx, l.sc, "webshop", "shop"); err != nil {
		t.Fatal(err)
	}
	if teams, _ := l.st.AppTeams(ctx, l.sc); teams["shop"] != "team-shop" || teams["webshop"] != "" {
		t.Fatalf("app teams after rename %v", teams)
	}
	// A service appearing in it gets the team; one also in an application with another
	// team gets none.
	cart, _ := l.st.EnsureService(ctx, l.sc, "cart")
	run(cart, "prod-b", "shop-frontend")
	both, _ := l.st.EnsureService(ctx, l.sc, "cache")
	run(both, "prod-a", "shop-frontend")
	run(both, "prod-b", "identity")
	_ = l.st.SetAppTeam(ctx, l.sc, "identity", "platform")
	named, err := FillOwners(ctx, l.st, l.sc)
	if err != nil {
		t.Fatal(err)
	}
	owner := func(s store.Service) string { got, _ := l.st.GetService(ctx, l.sc, s.ID); return got.Owner }
	if owner(l.svc) != "team-shop" || owner(cart) != "team-shop" || owner(both) != "" {
		t.Fatalf("owners web=%q cart=%q cache=%q (named %v)", owner(l.svc), owner(cart), owner(both), named)
	}
	// An owner set by hand stays.
	_, _ = l.st.SetOwners(ctx, l.sc, []string{cart.ID}, "someone")
	_, _ = FillOwners(ctx, l.st, l.sc)
	if owner(cart) != "someone" {
		t.Fatal("an owner set by hand was replaced")
	}
}
