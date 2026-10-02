// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

type fakeTags struct {
	mu    sync.Mutex
	tags  map[string][]string
	calls map[string]int
}

func (f *fakeTags) ListTags(_ context.Context, repo string, _ registry.Credentials) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[repo]++
	return f.tags[repo], nil
}

type lab struct {
	t       *testing.T
	st      *store.Store
	sc      store.Scope
	envs    map[string]store.Environment
	targets map[string]store.Target
	svc     store.Service
	private store.Service
	tags    *fakeTags
	checker *Checker
}

func newLab(t *testing.T) *lab {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	l := &lab{t: t, st: st, sc: ws.Scope(), envs: map[string]store.Environment{}, targets: map[string]store.Target{}}
	for i, name := range []string{"dev", "staging", "prod"} {
		e, _ := st.CreateEnvironment(ctx, l.sc, name, (i+1)*10)
		l.envs[name] = e
	}
	for _, tg := range []struct{ name, env string }{{"dev-1", "dev"}, {"stg-1", "staging"}, {"prod-a", "prod"}, {"prod-b", "prod"}} {
		target, err := st.CreateTarget(ctx, store.Target{Scope: l.sc, EnvironmentID: l.envs[tg.env].ID, Platform: "kubernetes", Name: tg.name})
		if err != nil {
			t.Fatal(err)
		}
		l.targets[tg.name] = target
	}
	l.svc, _ = st.EnsureService(ctx, l.sc, "web")
	l.private, _ = st.EnsureService(ctx, l.sc, "payments")
	l.tags = &fakeTags{calls: map[string]int{}, tags: map[string][]string{
		"docker.io/library/nginx": strings.Fields("latest 1.26.2 1.27.2 1.27.3 1.28.0 1.28.0-alpine 1.29.0-rc.1 stable"),
	}}
	l.checker = NewChecker(st, l.tags, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	return l
}

// run sets what each target runs: target -> tag of nginx ("" = nothing).
func (l *lab) run(versions map[string]string) {
	l.t.Helper()
	ctx := context.Background()
	for name, target := range l.targets {
		existing, _ := l.st.ListTargetInstances(ctx, l.sc, target.ID)
		ch := store.SnapshotChanges{Scope: l.sc, TargetID: target.ID, SnapshotID: store.NewID(), At: time.Now()}
		for _, i := range existing {
			if i.Active() {
				ch.Remove = append(ch.Remove, i.ID)
			}
		}
		if tag := versions[name]; tag != "" {
			ch.Remove = nil
			for _, i := range existing {
				if i.Active() && i.Tag != tag {
					ch.Remove = append(ch.Remove, i.ID)
				}
			}
			ch.Upsert = []store.Instance{{
				TargetID: target.ID, EnvironmentID: target.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "web",
				WorkloadKind: "deployment", WorkloadName: "web", ContainerName: "nginx", Image: "nginx:" + tag, Tag: tag,
				Running: 2, IsMain: true,
			}, {
				TargetID: target.ID, EnvironmentID: target.EnvironmentID, ServiceID: l.private.ID, WorkloadID: "pay",
				WorkloadKind: "deployment", WorkloadName: "pay", ContainerName: "app",
				Image: "registry.internal.example/team/payments:1.0.0", Tag: "1.0.0", Running: 1, IsMain: true,
			}}
		}
		if err := l.st.ApplySnapshot(ctx, ch); err != nil {
			l.t.Fatal(err)
		}
	}
}

func (l *lab) drifts() string {
	l.t.Helper()
	open, err := l.st.OpenDrifts(context.Background(), l.sc)
	if err != nil {
		l.t.Fatal(err)
	}
	envName := map[string]string{}
	for n, e := range l.envs {
		envName[e.ID] = n
	}
	var parts []string
	for _, d := range open {
		var det DriftDetail
		_ = json.Unmarshal(d.Detail, &det)
		parts = append(parts, envName[d.EnvironmentID]+":"+d.Kind+":"+det.Running+"<"+det.Other)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func (l *lab) events(types ...string) string {
	evs, _ := l.st.ListEvents(context.Background(), l.sc, store.EventFilter{Types: types})
	var parts []string
	for _, e := range evs {
		parts = append(parts, e.Type+":"+e.FromVersion+"->"+e.ToVersion+"("+e.Note+")")
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func TestUpstreamAndDrift(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	l.run(map[string]string{"dev-1": "1.27.3", "stg-1": "1.27.2", "prod-a": "1.27.2", "prod-b": "1.26.2"})

	// First check is a baseline: releases recorded, no new_release.
	if err := l.checker.CheckUpstreams(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	if ev := l.events("new_release"); ev != "" {
		t.Fatalf("baseline produced %s", ev)
	}
	rel, _ := l.st.ListReleases(ctx, l.sc, l.svc.ID)
	if len(rel) != 4 { // 1.26.2 1.27.2 1.27.3 1.28.0: no alpine, no rc, no latest
		t.Fatalf("releases %+v", rel)
	}
	if l.tags.calls["registry.internal.example/team/payments"] != 0 {
		t.Fatal("server checked a private registry")
	}
	if at, msg, _ := l.st.UpstreamStatus(ctx, l.sc, l.svc.ID); at.IsZero() || msg != "" {
		t.Fatalf("upstream status %v %q", at, msg)
	}

	if err := l.checker.EvaluateDrift(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	want := "dev:upstream:1.27.3<1.28.0 prod:inconsistent:1.27.2< prod:upstream:1.27.2<1.28.0 " +
		"staging:env:1.27.2<1.27.3 staging:upstream:1.27.2<1.28.0"
	if got := l.drifts(); got != want {
		t.Fatalf("drifts\n got: %s\nwant: %s", got, want)
	}
	// Upstream drift is announced at once; inconsistent after 15 minutes, env after 7 days.
	announced := func() int { return strings.Count(l.events("drift_detected"), "drift_detected") }
	if n := announced(); n != 3 {
		t.Fatalf("announced at once: %s", l.events("drift_detected"))
	}
	clock := time.Now().UTC()
	l.checker.now = func() time.Time { return clock }
	clock = clock.Add(16 * time.Minute)
	_ = l.checker.EvaluateDrift(ctx, l.sc)
	if n := announced(); n != 4 || !strings.Contains(l.events("drift_detected"), "(inconsistent)") {
		t.Fatalf("after 16 minutes: %s", l.events("drift_detected"))
	}
	clock = clock.Add(7 * 24 * time.Hour)
	_ = l.checker.EvaluateDrift(ctx, l.sc)
	if n := announced(); n != 5 {
		t.Fatalf("after 7 days: %s", l.events("drift_detected"))
	}

	// Re-evaluating the same state changes nothing.
	_ = l.checker.EvaluateDrift(ctx, l.sc)
	if n := announced(); n != 5 {
		t.Fatal("drift reported twice")
	}

	// A new upstream release: one new_release from the prod version.
	l.tags.tags["docker.io/library/nginx"] = append(l.tags.tags["docker.io/library/nginx"], "1.28.1")
	if err := l.checker.CheckService(ctx, l.sc, l.svc.ID); err != nil {
		t.Fatal(err)
	}
	if ev := l.events("new_release"); ev != "new_release:1.27.2->1.28.1(minor)" {
		t.Fatalf("new release: %s", ev)
	}

	// Staging catches up and prod becomes consistent: those drifts resolve.
	l.run(map[string]string{"dev-1": "1.27.3", "stg-1": "1.27.3", "prod-a": "1.27.3", "prod-b": "1.27.3"})
	_ = l.checker.EvaluateDrift(ctx, l.sc)
	want = "dev:upstream:1.27.3<1.28.1 prod:upstream:1.27.3<1.28.1 staging:upstream:1.27.3<1.28.1"
	if got := l.drifts(); got != want {
		t.Fatalf("after catch-up\n got: %s\nwant: %s", got, want)
	}
	if ev := l.events("drift_resolved"); strings.Count(ev, "drift_resolved") != 2 {
		t.Fatalf("resolved events: %s", ev)
	}

	// Tracking majors only silences minor upstream lag.
	l.svc.VersionPolicy = json.RawMessage(`{"track":"major"}`)
	if err := l.st.UpdateService(ctx, l.svc); err != nil {
		t.Fatal(err)
	}
	_ = l.checker.EvaluateDrift(ctx, l.sc)
	if got := l.drifts(); got != "" {
		t.Fatalf("with track major: %s", got)
	}
}

func TestReferences(t *testing.T) {
	m := Matrix{Rows: []Row{{Service: store.Service{ID: "s", Upstream: ""}, Cells: []Cell{
		{Versions: []RunningVersion{{Tag: "1.0"}}}, {}, {Versions: []RunningVersion{{Tag: "0.9"}}},
	}}}}
	refs := References(m, []store.Instance{
		{ServiceID: "s", IsMain: true, Image: "ghcr.io/a/b:1.0"},
		{ServiceID: "s", IsMain: true, Image: "ghcr.io/a/b:0.9"},
		{ServiceID: "s", IsMain: false, Image: "envoyproxy/envoy:v1"},
	})
	if refs["s"].Repo != "ghcr.io/a/b" || refs["s"].Tag != "0.9" {
		t.Fatalf("refs %+v", refs)
	}
}

func TestShortDriftIsNotAnnounced(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	// Staging ahead of prod for a day: shown, but never announced or "resolved".
	l.run(map[string]string{"stg-1": "1.27.3", "prod-a": "1.27.2", "prod-b": "1.27.2"})
	_ = l.checker.EvaluateDrift(ctx, l.sc)
	if !strings.Contains(l.drifts(), "prod:env:") {
		t.Fatalf("env drift not open: %s", l.drifts())
	}
	l.run(map[string]string{"stg-1": "1.27.3", "prod-a": "1.27.3", "prod-b": "1.27.3"})
	_ = l.checker.EvaluateDrift(ctx, l.sc)
	if ev := l.events("drift_detected", "drift_resolved"); ev != "" {
		t.Fatalf("short drift produced events: %s", ev)
	}

	// A policy can shorten the delay.
	l.svc.VersionPolicy = json.RawMessage(`{"drift_alert_after":{"env":"0s"}}`)
	_ = l.st.UpdateService(ctx, l.svc)
	l.run(map[string]string{"stg-1": "1.27.3", "prod-a": "1.27.2", "prod-b": "1.27.2"})
	_ = l.checker.EvaluateDrift(ctx, l.sc)
	if ev := l.events("drift_detected"); !strings.Contains(ev, "(env)") {
		t.Fatalf("policy delay ignored: %s", ev)
	}
	if _, err := ParsePolicy([]byte(`{"drift_alert_after":{"env":"soon"}}`)); err == nil {
		t.Fatal("bad duration accepted")
	}
	if _, err := ParsePolicy([]byte(`{"drift_alert_after":{"weird":"1h"}}`)); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
