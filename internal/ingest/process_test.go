// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/store/storetest"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

type world struct {
	t       *testing.T
	st      *store.Store
	svc     *Service
	ws      store.Workspace
	prod    store.Environment
	staging store.Environment
	agent   store.Agent
	target  store.Target
	clock   time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	staging, _ := st.CreateEnvironment(ctx, ws.Scope(), "staging", 20)
	prod, _ := st.CreateEnvironment(ctx, ws.Scope(), "prod", 30)
	agent, _ := st.CreateAgent(ctx, ws.Scope(), "a", "h")
	target, err := st.CreateTarget(ctx, store.Target{
		Scope: ws.Scope(), EnvironmentID: prod.ID, AgentID: agent.ID,
		Platform: "kubernetes", Name: "prod-eu-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &world{
		t: t, st: st, svc: New(st, slog.New(slog.NewTextHandler(io.Discard, nil))), ws: ws, prod: prod,
		staging: staging, agent: agent, target: target, clock: time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC),
	}
}

var digests = map[string]string{}

func digest(name string) *string {
	if _, ok := digests[name]; !ok {
		digests[name] = "sha256:" + strings.Repeat(fmt.Sprintf("%x", len(digests)%16), 64)
	}
	d := digests[name]
	return &d
}

func payments(versions map[string]int, retag bool) agentproto.Workload {
	ns := "payments"
	w := agentproto.Workload{
		ID: "uid-pay", Kind: agentproto.Deployment, Namespace: &ns, Name: "payments-api",
		Labels: map[string]string{"app.kubernetes.io/name": "payments-api"},
	}
	tags := make([]string, 0, len(versions))
	for v := range versions {
		tags = append(tags, v)
	}
	sort.Strings(tags)
	for _, v := range tags {
		d := digest(v)
		if retag {
			d = digest(v + "-retag")
		}
		w.Containers = append(w.Containers, agentproto.Container{Name: "app", Image: "ghcr.io/acme/payments-api:" + v, Digest: d, Running: versions[v]})
	}
	w.Containers = append(w.Containers, agentproto.Container{Name: "envoy", Image: "envoyproxy/envoy:v1.31.2", Running: 3})
	return w
}

func worker(labels map[string]string) agentproto.Workload {
	ns := "jobs"
	return agentproto.Workload{
		ID: "uid-worker", Kind: agentproto.Deployment, Namespace: &ns, Name: "worker-v2",
		Labels: labels, Containers: []agentproto.Container{{Name: "main", Image: "ghcr.io/acme/worker:2.0.0", Running: 1}},
	}
}

// send stores and processes a snapshot collected d after the previous one.
func (w *world) send(complete bool, workloads ...agentproto.Workload) []store.Event {
	w.t.Helper()
	w.clock = w.clock.Add(5 * time.Minute)
	return w.sendAt(w.clock, complete, workloads...)
}

func (w *world) sendAt(at time.Time, complete bool, workloads ...agentproto.Workload) []store.Event {
	w.t.Helper()
	ctx := context.Background()
	snap := agentproto.Snapshot{SnapshotID: store.NewID(), TargetID: w.target.ID, CollectedAt: at, Complete: complete, Workloads: workloads}
	raw, _ := json.Marshal(snap)
	if _, err := w.svc.Snapshot(ctx, w.agent, snap, raw); err != nil {
		w.t.Fatal(err)
	}
	before, _ := w.st.ListEvents(ctx, w.ws.Scope(), store.EventFilter{Limit: 1000})
	if _, err := w.svc.ProcessPending(ctx); err != nil {
		w.t.Fatal(err)
	}
	after, _ := w.st.ListEvents(ctx, w.ws.Scope(), store.EventFilter{Limit: 1000})
	return after[:len(after)-len(before)]
}

func describe(evs []store.Event) string {
	var parts []string
	for _, e := range evs {
		s := e.Type + ":" + e.FromVersion + "->" + e.ToVersion
		if e.Note != "" {
			s += "(" + e.Note + ")"
		}
		parts = append(parts, s)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func (w *world) active() map[string]store.Instance {
	w.t.Helper()
	all, err := w.st.ListActiveInstances(context.Background(), w.ws.Scope())
	if err != nil {
		w.t.Fatal(err)
	}
	m := map[string]store.Instance{}
	for _, i := range all {
		m[i.WorkloadName+"/"+i.ContainerName+":"+i.Tag] = i
	}
	return m
}

func TestProcessLifecycle(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	// 1. Baseline: instances recorded, service created from the label, no events.
	if evs := w.send(true, payments(map[string]int{"1.4.2": 3}, false), worker(nil)); len(evs) != 0 {
		t.Fatalf("baseline produced events: %s", describe(evs))
	}
	act := w.active()
	pay, env := act["payments-api/app:1.4.2"], act["payments-api/envoy:v1.31.2"]
	svc, err := w.st.GetServiceByName(ctx, w.ws.Scope(), "payments-api")
	if err != nil || pay.ServiceID != svc.ID || !pay.IsMain || env.IsMain || pay.EnvironmentID != w.prod.ID || pay.Running != 3 {
		t.Fatalf("payments instances: %+v / %+v (%v)", pay, env, err)
	}
	if wk := act["worker-v2/main:2.0.0"]; wk.ServiceID != "" || wk.SuggestedService != "worker" || !wk.IsMain {
		t.Fatalf("worker should be in the inbox with a suggestion: %+v", wk)
	}

	// 2. Rollout starts: 1.4.2 still dominant, no event.
	if evs := w.send(true, payments(map[string]int{"1.4.2": 2, "1.5.0": 1}, false), worker(nil)); len(evs) != 0 {
		t.Fatalf("early rollout produced events: %s", describe(evs))
	}
	// 3. Majority flips: one version_changed.
	if got := describe(w.send(true, payments(map[string]int{"1.4.2": 1, "1.5.0": 2}, false), worker(nil))); got != "version_changed:1.4.2->1.5.0" {
		t.Fatalf("flip: %s", got)
	}
	// 4. Rollout done: old instance removed, no further event.
	if evs := w.send(true, payments(map[string]int{"1.5.0": 3}, false), worker(nil)); len(evs) != 0 {
		t.Fatalf("rollout end: %s", describe(evs))
	}
	if _, ok := w.active()["payments-api/app:1.4.2"]; ok {
		t.Fatal("1.4.2 instance still active")
	}
	// 5. Same tag, new digest: retag.
	if got := describe(w.send(true, payments(map[string]int{"1.5.0": 3}, true), worker(nil))); got != "version_changed:1.5.0->1.5.0(retag)" {
		t.Fatalf("retag: %s", got)
	}
	// 6. Incomplete snapshot without the worker: nothing removed.
	if evs := w.send(false, payments(map[string]int{"1.5.0": 3}, true)); len(evs) != 0 {
		t.Fatalf("incomplete snapshot: %s", describe(evs))
	}
	if _, ok := w.active()["worker-v2/main:2.0.0"]; !ok {
		t.Fatal("worker removed by an incomplete snapshot")
	}
	// 7. Complete without the worker, with a new workload labelled for staging.
	ns := "search"
	search := agentproto.Workload{
		ID: "uid-search", Kind: agentproto.Statefulset, Namespace: &ns, Name: "search",
		Labels:     map[string]string{"goliash.service": "search", "goliash.env": "staging"},
		Containers: []agentproto.Container{{Name: "es", Image: "docker.elastic.co/elasticsearch/elasticsearch:8.15.1", Running: 3}},
	}
	if got := describe(w.send(true, payments(map[string]int{"1.5.0": 3}, true), search)); got != "deployed:->8.15.1 removed:2.0.0->" {
		t.Fatalf("deploy/remove: %s", got)
	}
	if es := w.active()["search/es:8.15.1"]; es.EnvironmentID != w.staging.ID {
		t.Fatalf("goliash.env not applied: %+v", es)
	}

	// 8. A late snapshot from before the last one changes nothing.
	if evs := w.sendAt(w.clock.Add(-time.Hour), true, worker(nil)); len(evs) != 0 {
		t.Fatalf("out-of-date snapshot: %s", describe(evs))
	}
	if _, ok := w.active()["worker-v2/main:2.0.0"]; ok {
		t.Fatal("out-of-date snapshot resurrected the worker")
	}

	// 9. A mapping rule maps the worker when it comes back; events carry the service.
	wsvc, _ := w.st.EnsureService(ctx, w.ws.Scope(), "worker")
	if _, err := w.st.CreateMappingRule(ctx, store.MappingRule{
		Scope: w.ws.Scope(), MatchType: "image_repo",
		Pattern: `ghcr\.io/acme/worker`, ServiceID: wsvc.ID,
	}); err != nil {
		t.Fatal(err)
	}
	evs := w.send(true, payments(map[string]int{"1.5.0": 3}, true), search, worker(nil))
	if describe(evs) != "deployed:->2.0.0" || evs[0].ServiceID != wsvc.ID {
		t.Fatalf("mapped worker: %s %+v", describe(evs), evs)
	}

	// History for the payments service, newest first.
	hist, err := w.st.ListEvents(ctx, w.ws.Scope(), store.EventFilter{ServiceID: svc.ID})
	if err != nil || describe(hist) != "version_changed:1.4.2->1.5.0 version_changed:1.5.0->1.5.0(retag)" || !hist[0].At.After(hist[1].At) {
		t.Fatalf("history: %s %v", describe(hist), err)
	}
}

func TestSidecarChangesAreQuiet(t *testing.T) {
	w := newWorld(t)
	w.send(true, payments(map[string]int{"1.4.2": 3}, false))
	p := payments(map[string]int{"1.4.2": 3}, false)
	p.Containers[1].Image = "envoyproxy/envoy:v1.32.0"
	if evs := w.send(true, p); len(evs) != 0 {
		t.Fatalf("sidecar upgrade produced events: %s", describe(evs))
	}
}

// A stale agent's next heartbeat reports it back, with its heartbeat before the gap.
func TestAgentBack(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	sc := w.ws.Scope()
	var back []store.Agent
	w.svc.OnAgentBack(func(a store.Agent) { back = append(back, a) })

	if _, err := w.svc.Heartbeat(ctx, w.agent, agentproto.Heartbeat{}); err != nil {
		t.Fatal(err)
	}
	if len(back) != 0 {
		t.Fatal("a live agent reported back")
	}
	if _, err := w.st.MarkStaleAgents(ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	before, _ := w.st.GetAgent(ctx, sc, w.agent.ID) // as the API loads it for the next request
	if _, err := w.svc.Heartbeat(ctx, before, agentproto.Heartbeat{}); err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].StaleSince.IsZero() || back[0].LastSeenAt.IsZero() {
		t.Fatalf("back %+v", back)
	}
	if _, err := w.svc.Heartbeat(ctx, before, agentproto.Heartbeat{}); err != nil || len(back) != 1 {
		t.Fatal("reported back twice")
	}
}

// With automatic mapping, a workload of a new image goes to its suggested service at once, with a rule;
// sidecars stay out of it.
func TestAutoMap(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	sc := w.ws.Scope()
	if err := w.st.SetWorkspaceAutoMap(ctx, sc.OrgID, sc.WorkspaceID, true); err != nil {
		t.Fatal(err)
	}
	w.send(true, payments(map[string]int{"1.4.2": 3}, false), worker(nil))
	act := w.active()
	svc, err := w.st.GetServiceByName(ctx, sc, "worker")
	if err != nil || act["worker-v2/main:2.0.0"].ServiceID != svc.ID {
		t.Fatalf("worker not mapped: %+v %v", act["worker-v2/main:2.0.0"], err)
	}
	if env := act["payments-api/envoy:v1.31.2"]; env.ServiceID == "" || env.IsMain {
		t.Fatalf("sidecar: %+v", env)
	}
	if _, err := w.st.GetServiceByName(ctx, sc, "envoy"); err == nil {
		t.Fatal("a service was made for a sidecar")
	}
	rules, _ := w.st.ListMappingRules(ctx, sc)
	if len(rules) != 1 || rules[0].MatchType != "image_repo" || rules[0].ServiceID != svc.ID {
		t.Fatalf("rules %+v", rules)
	}
	// Turned off, the rule keeps the image mapped; nothing is made twice.
	_ = w.st.SetWorkspaceAutoMap(ctx, sc.OrgID, sc.WorkspaceID, false)
	w.send(true, payments(map[string]int{"1.4.2": 3}, false), worker(nil))
	if act := w.active(); act["worker-v2/main:2.0.0"].ServiceID != svc.ID {
		t.Fatal("mapping lost when turned off")
	}
	if rules, _ := w.st.ListMappingRules(ctx, sc); len(rules) != 1 {
		t.Fatalf("rules %d", len(rules))
	}
}

// How a workload can be reached from outside is stored with its instances and follows each snapshot; a
// goliash.exposed label overrides what the agent read.
func TestExposed(t *testing.T) {
	w := newWorld(t)
	pay := payments(map[string]int{"1.4.2": 1}, false)
	pay.Exposed = []string{"ingress pay.example.com"}
	wk := worker(map[string]string{"goliash.exposed": "internet"})
	w.send(true, pay, wk)
	act := w.active()
	if e := act["payments-api/app:1.4.2"].Exposed; len(e) != 1 || e[0] != "ingress pay.example.com" {
		t.Fatalf("payments exposed %v", e)
	}
	if e := act["worker-v2/main:2.0.0"].Exposed; len(e) != 1 || e[0] != "label goliash.exposed" {
		t.Fatalf("worker exposed %v", e)
	}
	pay.Exposed = []string{"ingress pay.example.com", "LoadBalancer service payments/pay"}
	pay.Labels["goliash.exposed"] = "internal"
	w.send(true, pay, worker(nil))
	act = w.active()
	if e := act["payments-api/app:1.4.2"].Exposed; len(e) != 0 {
		t.Errorf("labelled internal, still exposed %v", e)
	}
	if e := act["worker-v2/main:2.0.0"].Exposed; len(e) != 0 {
		t.Errorf("no longer exposed, still %v", e)
	}
}

// A rolling update whose new container restarts while the old one still runs is one change forward, not a
// rollback and a redeploy; a real rollback is still one.
func TestRollingUpdateFlap(t *testing.T) {
	w := newWorld(t)
	w.send(true, payments(map[string]int{"1.4.2": 1}, false))
	var got []string
	for _, step := range []map[string]int{
		{"1.4.2": 1, "1.5.0": 1}, // both up
		{"1.4.2": 1, "1.5.0": 0}, // the new one restarts
		{"1.4.2": 1, "1.5.0": 1},
		{"1.5.0": 1}, // done
	} {
		got = append(got, describe(w.send(true, payments(step, false))))
	}
	// Said as soon as the new version takes over (a stuck rollout gets its rollback offer), and only once.
	if strings.Join(got, " | ") != "version_changed:1.4.2->1.5.0 |  |  | " {
		t.Errorf("rolling update: %q", got)
	}
	// Rolled back through a rolling update: one change back.
	got = nil
	for _, step := range []map[string]int{{"1.4.2": 1, "1.5.0": 1}, {"1.4.2": 1}} {
		if d := describe(w.send(true, payments(step, false))); d != "" {
			got = append(got, d)
		}
	}
	if strings.Join(got, " | ") != "version_changed:1.5.0->1.4.2" {
		t.Errorf("rollback: %v", got)
	}
}

// A snapshot that changes nothing writes no instance again; the instances' last sighting still moves on.
func TestUnchangedSnapshotWritesNothing(t *testing.T) {
	w := newWorld(t)
	w.send(true, payments(map[string]int{"1.4.2": 3}, false))
	before := w.active()["payments-api/app:1.4.2"]
	w.send(true, payments(map[string]int{"1.4.2": 3}, false))
	after := w.active()["payments-api/app:1.4.2"]
	if after.ID != before.ID || !after.LastSeenAt.After(before.LastSeenAt) {
		t.Fatalf("last sighting did not move: %v → %v", before.LastSeenAt, after.LastSeenAt)
	}
	// A change is still written.
	w.send(true, payments(map[string]int{"1.4.2": 2}, false))
	if got := w.active()["payments-api/app:1.4.2"]; got.Running != 2 {
		t.Fatalf("running %d", got.Running)
	}
}
