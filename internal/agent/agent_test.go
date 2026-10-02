// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

const testToken = "glsh_agent_0123456789abcdefghijklmnopqrstuvwxyz" //nolint:gosec // fake token for tests

// fakeServer implements the agent protocol in memory.
type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	targets    []agentproto.Target
	etag       string
	snapshots  []agentproto.Snapshot
	heartbeats []agentproto.Heartbeat
	registered int
	registries []agentproto.RegistryCheck
	results    []agentproto.RegistryResults
	down       bool // answer 503 to snapshots
	gone       map[string]bool
	authStatus int
}

func newFakeServer(t *testing.T, targets ...agentproto.Target) *fakeServer {
	f := &fakeServer{t: t, targets: targets, etag: `"v1"`, gone: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /agent/v1/register", func(w http.ResponseWriter, r *http.Request) {
		if !f.auth(w, r) {
			return
		}
		f.mu.Lock()
		f.registered++
		f.mu.Unlock()
		writeJSON(w, 200, agentproto.RegisterResponse{AgentID: "A", WorkspaceID: "W", ServerTime: time.Now()})
	})
	mux.HandleFunc("GET /agent/v1/config", func(w http.ResponseWriter, r *http.Request) {
		if !f.auth(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("ETag", f.etag)
		if r.Header.Get("If-None-Match") == f.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, 200, agentproto.AgentConfig{HeartbeatIntervalSeconds: 10, Targets: f.targets, Registries: f.registries})
	})
	mux.HandleFunc("POST /agent/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if !f.auth(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("snapshot not gzip-encoded")
		}
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("gzip: %v", err)
			return
		}
		var snap agentproto.Snapshot
		if err := json.NewDecoder(zr).Decode(&snap); err != nil {
			t.Errorf("decode snapshot: %v", err)
			return
		}
		if f.gone[snap.TargetID] {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"title":"Unknown target","status":404}`))
			return
		}
		f.snapshots = append(f.snapshots, snap)
		writeJSON(w, http.StatusAccepted, agentproto.SnapshotAck{SnapshotID: snap.SnapshotID, Status: agentproto.Accepted})
	})
	mux.HandleFunc("POST /agent/v1/registry-results", func(w http.ResponseWriter, r *http.Request) {
		if !f.auth(w, r) {
			return
		}
		var res agentproto.RegistryResults
		_ = json.NewDecoder(r.Body).Decode(&res)
		f.mu.Lock()
		f.results = append(f.results, res)
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /agent/v1/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		if !f.auth(w, r) {
			return
		}
		var hb agentproto.Heartbeat
		_ = json.NewDecoder(r.Body).Decode(&hb)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.heartbeats = append(f.heartbeats, hb)
		writeJSON(w, 200, agentproto.HeartbeatResponse{ConfigEtag: f.etag})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) auth(w http.ResponseWriter, r *http.Request) bool {
	f.mu.Lock()
	status := f.authStatus
	f.mu.Unlock()
	if status != 0 {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"title":"rejected","status":401,"detail":"unknown or revoked token"}`))
		return false
	}
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		f.t.Errorf("missing or wrong token: %q", r.Header.Get("Authorization"))
	}
	return true
}

func (f *fakeServer) setTargets(etag string, targets ...agentproto.Target) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.etag, f.targets = etag, targets
}

func (f *fakeServer) snapshotCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.snapshots)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fakeCollector returns a fixed result and counts calls.
type fakeCollector struct {
	calls    atomic.Int32
	complete bool
	fail     error
	changes  chan struct{} // when set, the collector is a Watcher
}

func (c *fakeCollector) Collect(context.Context) (collectors.Result, error) {
	c.calls.Add(1)
	if c.fail != nil {
		return collectors.Result{}, c.fail
	}
	res := collectors.Result{Complete: c.complete, Workloads: []agentproto.Workload{{
		ID: "uid-1", Kind: agentproto.Deployment, Name: "payments-api",
		Containers: []agentproto.Container{{Name: "app", Image: "ghcr.io/acme/payments-api:1.4.2", Running: 2}},
	}}}
	if !c.complete {
		res.Errors = []string{"namespace billing: forbidden"}
	}
	return res, nil
}

type watchingCollector struct{ *fakeCollector }

func (c watchingCollector) Watch(ctx context.Context, changed func()) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.changes:
			changed()
		}
	}
}

func target(id string, platform agentproto.Platform) agentproto.Target {
	return agentproto.Target{ID: id, Name: "t-" + id, Platform: platform, PollIntervalSeconds: 3600}
}

func newTestAgent(t *testing.T, f *fakeServer, factories map[agentproto.Platform]collectors.Factory) *Agent {
	t.Helper()
	a, err := New(Options{
		ServerURL:      f.srv.URL,
		Token:          testToken,
		DataDir:        t.TempDir(),
		Collectors:     factories,
		ConfigInterval: 50 * time.Millisecond,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func factoryFor(c collectors.Collector) collectors.Factory {
	return func(context.Context, agentproto.Target) (collectors.Collector, error) { return c, nil }
}

func runAgent(t *testing.T, a *Agent) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- a.Run(ctx) }()
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			cancel()
			err = <-errc
		})
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAgentShipsSnapshotsAndHeartbeats(t *testing.T) {
	f := newFakeServer(t, target("T1", agentproto.Kubernetes), target("T2", agentproto.Nomad))
	col := &fakeCollector{complete: true}
	a := newTestAgent(t, f, map[agentproto.Platform]collectors.Factory{agentproto.Kubernetes: factoryFor(col)})
	runAgent(t, a)

	eventually(t, "snapshot", func() bool { return f.snapshotCount() == 1 })
	f.mu.Lock()
	snap := f.snapshots[0]
	reg := f.registered
	f.mu.Unlock()
	if reg != 1 || snap.TargetID != "T1" || !snap.Complete || len(snap.Workloads) != 1 || snap.AgentVersion == nil {
		t.Fatalf("registered=%d snapshot=%+v", reg, snap)
	}

	// The next heartbeat reports T1 healthy and T2 failing (no nomad collector here).
	eventually(t, "heartbeat with statuses", func() bool {
		_ = a.heartbeat(context.Background())
		f.mu.Lock()
		defer f.mu.Unlock()
		hb := f.heartbeats[len(f.heartbeats)-1]
		return len(hb.Collectors) == 2 && hb.Collectors[0].LastSnapshotID != nil
	})
	f.mu.Lock()
	hb := f.heartbeats[len(f.heartbeats)-1]
	f.mu.Unlock()
	if hb.Collectors[0].TargetID != "T1" || hb.Collectors[0].Status != agentproto.Ok || *hb.Collectors[0].LastSnapshotID != snap.SnapshotID {
		t.Fatalf("T1 status %+v", hb.Collectors[0])
	}
	if hb.Collectors[1].Status != agentproto.Failing || hb.Collectors[1].LastError == nil {
		t.Fatalf("T2 status %+v", hb.Collectors[1])
	}
}

func TestAgentBuffersWhileServerIsDown(t *testing.T) {
	f := newFakeServer(t, target("T1", agentproto.Kubernetes))
	f.down = true
	col := &fakeCollector{complete: true, changes: make(chan struct{}, 1)}
	tg := target("T1", agentproto.Kubernetes)
	zero := 0
	tg.DebounceSeconds = &zero
	f.setTargets(`"v1"`, tg)
	a := newTestAgent(t, f, map[agentproto.Platform]collectors.Factory{
		agentproto.Kubernetes: factoryFor(watchingCollector{col}),
	})
	runAgent(t, a)

	// Initial collect plus two watch-triggered ones, all buffered.
	eventually(t, "first collect", func() bool { return col.calls.Load() == 1 })
	for i := int32(2); i <= 3; i++ {
		col.changes <- struct{}{}
		eventually(t, "watch-triggered collect", func() bool { return col.calls.Load() == i })
	}
	eventually(t, "3 buffered", func() bool { return a.outbox.len() == 3 })
	if f.snapshotCount() != 0 {
		t.Fatal("snapshots delivered while server was down")
	}

	f.mu.Lock()
	f.down = false
	f.mu.Unlock()
	select { // skip the backoff wait
	case a.outbox.notify <- struct{}{}:
	default:
	}
	eventually(t, "delivery after recovery", func() bool { return f.snapshotCount() == 3 })

	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 1; i < len(f.snapshots); i++ {
		if f.snapshots[i-1].SnapshotID >= f.snapshots[i].SnapshotID {
			t.Fatalf("snapshots out of order: %s then %s", f.snapshots[i-1].SnapshotID, f.snapshots[i].SnapshotID)
		}
	}
	if a.outbox.len() != 0 {
		t.Fatal("outbox not emptied")
	}
}

func TestAgentFollowsConfigChanges(t *testing.T) {
	f := newFakeServer(t, target("T1", agentproto.Kubernetes))
	var mu sync.Mutex
	built := map[string]int{}
	factory := func(_ context.Context, tg agentproto.Target) (collectors.Collector, error) {
		mu.Lock()
		built[tg.ID]++
		mu.Unlock()
		return &fakeCollector{complete: true}, nil
	}
	a := newTestAgent(t, f, map[agentproto.Platform]collectors.Factory{agentproto.Kubernetes: factory})
	runAgent(t, a)
	eventually(t, "T1 snapshot", func() bool { return f.snapshotCount() == 1 })

	// T1 removed, T2 added.
	f.setTargets(`"v2"`, target("T2", agentproto.Kubernetes))
	eventually(t, "T2 snapshot", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.snapshots) == 2 && f.snapshots[1].TargetID == "T2"
	})
	a.mu.Lock()
	_, t1 := a.runners["T1"]
	a.mu.Unlock()
	if t1 {
		t.Fatal("removed target still running")
	}

	// Changed settings for T2 restart its collector.
	changed := target("T2", agentproto.Kubernetes)
	changed.PollIntervalSeconds = 1800
	f.setTargets(`"v3"`, changed)
	eventually(t, "T2 rebuilt", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return built["T2"] == 2
	})
}

func TestAgentDropsSnapshotsForRemovedTarget(t *testing.T) {
	f := newFakeServer(t, target("T1", agentproto.Kubernetes))
	f.gone["T1"] = true
	a := newTestAgent(t, f, map[agentproto.Platform]collectors.Factory{
		agentproto.Kubernetes: factoryFor(&fakeCollector{complete: true}),
	})
	runAgent(t, a)
	eventually(t, "snapshot dropped", func() bool {
		ids, _ := a.outbox.list()
		return len(ids) == 0 && a.outbox.len() == 0
	})
	if f.snapshotCount() != 0 {
		t.Fatal("snapshot accepted for a removed target")
	}
}

func TestAgentDegradedAndFailingStatus(t *testing.T) {
	f := newFakeServer(t, target("T1", agentproto.Kubernetes), target("T2", agentproto.Ecs))
	a := newTestAgent(t, f, map[agentproto.Platform]collectors.Factory{
		agentproto.Kubernetes: factoryFor(&fakeCollector{complete: false}),
		agentproto.Ecs:        factoryFor(&fakeCollector{fail: errors.New("AccessDenied: ecs:ListClusters")}),
	})
	runAgent(t, a)
	eventually(t, "degraded snapshot", func() bool { return f.snapshotCount() == 1 })

	statuses := map[string]agentproto.CollectorStatus{}
	eventually(t, "statuses", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		for id, rt := range a.runners {
			statuses[id] = rt.runner.currentStatus()
		}
		return statuses["T1"].Status == agentproto.Degraded && statuses["T2"].Status == agentproto.Failing
	})
	if *statuses["T2"].LastError != "AccessDenied: ecs:ListClusters" || statuses["T2"].LastSnapshotID != nil {
		t.Fatalf("T2 %+v", statuses["T2"])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapshots[0].Complete || len(f.snapshots[0].Errors) != 1 {
		t.Fatalf("incomplete snapshot sent as %+v", f.snapshots[0])
	}
}

func TestAgentStopsOnRejectedToken(t *testing.T) {
	f := newFakeServer(t)
	f.authStatus = http.StatusUnauthorized
	a := newTestAgent(t, f, nil)
	err := a.Run(context.Background())
	if !fatal(err) {
		t.Fatalf("Run returned %v, want a fatal 401", err)
	}
}

func TestOutboxDropsOldest(t *testing.T) {
	ob, err := newOutbox(t.TempDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"01A", "01B", "01C"} {
		if _, err := ob.put(id, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	ids, _ := ob.list()
	if len(ids) != 2 || ids[0] != "01B" || ids[1] != "01C" {
		t.Fatalf("ids = %v", ids)
	}
	// Survives a restart.
	again, _ := newOutbox(ob.dir, 2)
	if again.len() != 2 {
		t.Fatal("buffer lost across restart")
	}
	if _, err := os.Stat(ob.path("01A")); !os.IsNotExist(err) {
		t.Fatal("oldest snapshot still on disk")
	}
}

func TestBackoff(t *testing.T) {
	b := newBackoff()
	prev := time.Duration(0)
	for range 12 {
		d := b.next(errors.New("x"))
		if d <= 0 || d > 6*time.Minute {
			t.Fatalf("delay %v out of range", d)
		}
		prev = d
	}
	if prev < 4*time.Minute {
		t.Fatalf("backoff did not grow to the cap: %v", prev)
	}
	if d := b.next(&statusError{Code: 429, RetryAfter: 7 * time.Second}); d != 7*time.Second {
		t.Fatalf("Retry-After ignored: %v", d)
	}
}

type fakeRegistry struct {
	mu    sync.Mutex
	creds map[string]string // repo -> password seen
}

func (r *fakeRegistry) ListTags(_ context.Context, repo string, creds registry.Credentials) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creds[repo] = creds.Username + ":" + creds.Password
	if repo == "registry.example.com/team/broken" {
		return nil, errors.New("registry denied access (401)")
	}
	return []string{"1.0.0", "1.1.0", "latest", "sha-abc"}, nil
}

func TestAgentChecksPrivateRegistries(t *testing.T) {
	ref, filter := "registry.example.com", `^\d+\.\d+\.\d+$`
	f := newFakeServer(t)
	f.registries = []agentproto.RegistryCheck{
		{Repository: "registry.example.com/team/api", CredentialsRef: &ref, TagFilter: &filter},
		{Repository: "registry.example.com/team/broken", CredentialsRef: &ref},
	}
	t.Setenv("GOLIASH_CREDENTIAL_REGISTRY_EXAMPLE_COM", "robot:s3cret")
	reg := &fakeRegistry{creds: map[string]string{}}
	a, err := New(Options{
		ServerURL: f.srv.URL, Token: testToken, DataDir: t.TempDir(), Registry: reg,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	runAgent(t, a)

	eventually(t, "registry results", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.results) > 0
	})
	f.mu.Lock()
	res := f.results[0].Results
	f.mu.Unlock()
	if len(res) != 2 || len(res[0].Tags) != 2 || res[0].Tags[1].Name != "1.1.0" || res[0].Error != nil {
		t.Fatalf("api result %+v", res[0])
	}
	if res[1].Error == nil || !strings.Contains(*res[1].Error, "401") {
		t.Fatalf("broken result %+v", res[1])
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.creds["registry.example.com/team/api"] != "robot:s3cret" {
		t.Fatalf("credentials not resolved locally: %v", reg.creds)
	}
}
