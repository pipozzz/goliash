// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
	"github.com/pipozzz/goliash/pkg/buildinfo"
)

// Options configure an Agent.
type Options struct {
	ServerURL string
	Token     string
	// DataDir holds snapshots waiting to be sent. They survive restarts.
	DataDir string
	// MaxBuffered is how many snapshots wait on disk at most (default 200).
	MaxBuffered int
	// Collectors builds a collector per platform. Targets on other platforms are reported as failing.
	Collectors map[agentproto.Platform]collectors.Factory
	// ConfigInterval is how often configuration is polled (default 1 minute).
	ConfigInterval time.Duration
	HTTPClient     *http.Client
	Logger         *slog.Logger
}

// Agent registers with the server, follows its configuration, runs a collector per
// target and ships snapshots and heartbeats.
type Agent struct {
	opts    Options
	client  *client
	outbox  *outbox
	log     *slog.Logger
	started time.Time

	mu      sync.Mutex
	etag    string
	cfg     agentproto.AgentConfig
	runners map[string]*runningTarget

	refresh chan struct{}
}

type runningTarget struct {
	spec   []byte // JSON of the target config, to detect changes
	runner *runner
	cancel context.CancelFunc
	done   chan struct{}
}

// New validates opts and returns an Agent.
func New(opts Options) (*Agent, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if opts.MaxBuffered <= 0 {
		opts.MaxBuffered = 200
	}
	if opts.ConfigInterval <= 0 {
		opts.ConfigInterval = time.Minute
	}
	if opts.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	c, err := newClient(opts.ServerURL, opts.Token, opts.HTTPClient)
	if err != nil {
		return nil, err
	}
	ob, err := newOutbox(opts.DataDir+"/outbox", opts.MaxBuffered)
	if err != nil {
		return nil, err
	}
	return &Agent{
		opts:    opts,
		client:  c,
		outbox:  ob,
		log:     opts.Logger,
		runners: map[string]*runningTarget{},
		refresh: make(chan struct{}, 1),
	}, nil
}

// Run blocks until ctx ends or the server rejects the agent for good (bad token,
// unsupported version), in which case it returns that error.
func (a *Agent) Run(ctx context.Context) error {
	a.started = time.Now()
	defer a.stopAll()

	if err := retry(ctx, a.log, "register", func() error { return a.register(ctx) }); err != nil {
		return err
	}
	if err := retry(ctx, a.log, "fetch config", func() error { return a.syncConfig(ctx) }); err != nil {
		return err
	}

	fatalErr := make(chan error, 1)
	go func() {
		if err := a.sendLoop(ctx); err != nil {
			fatalErr <- err
		}
	}()

	configTick := time.NewTicker(a.opts.ConfigInterval)
	defer configTick.Stop()
	hbTick := time.NewTicker(a.heartbeatInterval())
	defer hbTick.Stop()
	_ = a.heartbeat(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-fatalErr:
			return err
		case <-configTick.C:
		case <-a.refresh:
		case <-hbTick.C:
			if err := a.heartbeat(ctx); fatal(err) {
				return err
			}
			hbTick.Reset(a.heartbeatInterval())
			continue
		}
		if err := a.syncConfig(ctx); err != nil {
			if fatal(err) {
				return err
			}
			a.log.Warn("config refresh failed", "err", err)
		}
	}
}

func (a *Agent) register(ctx context.Context) error {
	host, _ := os.Hostname()
	platforms := make([]agentproto.Platform, 0, len(a.opts.Collectors))
	for p := range a.opts.Collectors {
		platforms = append(platforms, p)
	}
	sort.Slice(platforms, func(i, j int) bool { return platforms[i] < platforms[j] })
	started := a.started.UTC()
	resp, err := a.client.register(ctx, agentproto.RegisterRequest{
		Version: buildinfo.Version, Hostname: host, Platforms: platforms, StartedAt: &started,
	})
	if err != nil {
		return err
	}
	if skew := time.Since(resp.ServerTime); skew > time.Minute || skew < -time.Minute {
		a.log.Warn("clock differs from the server", "skew", skew.Round(time.Second))
	}
	a.log.Info("registered", "agent_id", resp.AgentID, "workspace_id", resp.WorkspaceID, "server", a.opts.ServerURL)
	return nil
}

// syncConfig fetches the configuration and starts, restarts or stops runners to match it.
func (a *Agent) syncConfig(ctx context.Context) error {
	a.mu.Lock()
	etag := a.etag
	a.mu.Unlock()

	cfg, newETag, changed, err := a.client.config(ctx, etag)
	if err != nil || !changed {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.etag, a.cfg = newETag, cfg

	want := map[string]agentproto.Target{}
	for _, t := range cfg.Targets {
		want[t.ID] = t
	}
	for id, rt := range a.runners {
		t, keep := want[id]
		if keep && bytes.Equal(rt.spec, mustJSON(t)) {
			continue
		}
		rt.cancel()
		<-rt.done
		delete(a.runners, id)
		if !keep {
			a.log.Info("target removed", "target_id", id)
		}
	}
	for id, t := range want {
		if _, running := a.runners[id]; running {
			continue
		}
		a.runners[id] = a.start(ctx, t)
	}
	a.log.Info("configuration applied", "targets", len(cfg.Targets))
	return nil
}

func (a *Agent) start(ctx context.Context, t agentproto.Target) *runningTarget {
	var r *runner
	factory, ok := a.opts.Collectors[t.Platform]
	if !ok {
		r = failingRunner(t, fmt.Sprintf("this agent has no %s collector", t.Platform))
		a.log.Warn("target platform not supported by this agent", "target", t.Name, "platform", t.Platform)
	} else if c, err := factory(ctx, t); err != nil {
		r = failingRunner(t, err.Error())
		a.log.Error("collector setup failed", "target", t.Name, "err", err)
	} else {
		r = newRunner(t, c, a.emit, a.log)
		a.log.Info("collecting target", "target", t.Name, "platform", t.Platform, "poll_seconds", t.PollIntervalSeconds)
	}
	rctx, cancel := context.WithCancel(ctx)
	rt := &runningTarget{spec: mustJSON(t), runner: r, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(rt.done)
		r.run(rctx)
	}()
	return rt
}

func (a *Agent) stopAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, rt := range a.runners {
		rt.cancel()
		<-rt.done
		delete(a.runners, id)
	}
}

// emit turns a collector result into a snapshot and queues it.
func (a *Agent) emit(t agentproto.Target, res collectors.Result) (string, error) {
	version := buildinfo.Version
	snap := agentproto.Snapshot{
		SnapshotID:   ulid.Make().String(),
		TargetID:     t.ID,
		CollectedAt:  time.Now().UTC(),
		AgentVersion: &version,
		Complete:     res.Complete,
		Errors:       res.Errors,
		Workloads:    res.Workloads,
	}
	if snap.Workloads == nil {
		snap.Workloads = []agentproto.Workload{}
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(snap); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	dropped, err := a.outbox.put(snap.SnapshotID, buf.Bytes())
	if dropped > 0 {
		a.log.Warn("snapshot buffer full; dropped oldest snapshots", "dropped", dropped)
	}
	return snap.SnapshotID, err
}

// sendLoop sends buffered snapshots in order, retrying while the server is unreachable.
func (a *Agent) sendLoop(ctx context.Context) error {
	b := newBackoff()
	for {
		wait, err := a.drain(ctx)
		if fatal(err) {
			return err
		}
		if err == nil {
			b.reset()
		} else {
			wait = b.next(err)
			a.log.Warn("sending snapshots failed; will retry", "err", err, "retry_in", wait.Round(time.Second),
				"buffered", a.outbox.len())
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-a.outbox.notify:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// drain sends every buffered snapshot. It stops at the first retryable error.
func (a *Agent) drain(ctx context.Context) (time.Duration, error) {
	ids, err := a.outbox.list()
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		gz, err := a.outbox.read(id)
		if err != nil {
			a.log.Error("unreadable buffered snapshot; dropping it", "snapshot_id", id, "err", err)
			_ = a.outbox.remove(id)
			continue
		}
		ack, err := a.client.snapshot(ctx, gz)
		switch {
		case err == nil:
			a.log.Debug("snapshot sent", "snapshot_id", id, "status", ack.Status)
		case retryable(err) || fatal(err):
			return 0, err
		case statusCode(err) == http.StatusNotFound:
			// The target is no longer ours; the snapshot is useless and our config is stale.
			a.log.Info("server no longer expects this target; dropping snapshot", "snapshot_id", id)
			a.requestRefresh()
		default:
			a.log.Error("server rejected snapshot; dropping it", "snapshot_id", id, "err", err)
		}
		if err := a.outbox.remove(id); err != nil {
			return 0, err
		}
	}
	return 10 * time.Minute, nil // idle; woken up by new snapshots
}

func (a *Agent) heartbeat(ctx context.Context) error {
	a.mu.Lock()
	statuses := make([]agentproto.CollectorStatus, 0, len(a.runners))
	for _, rt := range a.runners {
		statuses = append(statuses, rt.runner.currentStatus())
	}
	etag := a.etag
	a.mu.Unlock()
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].TargetID < statuses[j].TargetID })

	uptime := int(time.Since(a.started) / time.Second)
	buffered := a.outbox.len()
	resp, err := a.client.heartbeat(ctx, agentproto.Heartbeat{
		SentAt: time.Now().UTC(), UptimeSeconds: &uptime, BufferedSnapshots: &buffered, Collectors: statuses,
	})
	if err != nil {
		if ctx.Err() == nil {
			a.log.Warn("heartbeat failed", "err", err)
		}
		return err
	}
	if resp.ConfigEtag != etag {
		a.requestRefresh()
	}
	return nil
}

func (a *Agent) heartbeatInterval() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.cfg.HeartbeatIntervalSeconds; s >= 10 {
		return time.Duration(s) * time.Second
	}
	return time.Minute
}

func (a *Agent) requestRefresh() {
	select {
	case a.refresh <- struct{}{}:
	default:
	}
}

// retry calls fn until it succeeds, ctx ends, or it fails fatally.
func retry(ctx context.Context, log *slog.Logger, what string, fn func() error) error {
	b := newBackoff()
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if fatal(err) || ctx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%s: %w", what, err)
		}
		wait := b.next(err)
		log.Warn(what+" failed; retrying", "err", err, "retry_in", wait.Round(time.Second))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// backoff grows from 1 s to 5 min with jitter and honours Retry-After.
type backoff struct{ cur time.Duration }

func newBackoff() *backoff { return &backoff{} }

func (b *backoff) reset() { b.cur = 0 }

func (b *backoff) next(err error) time.Duration {
	var se *statusError
	if errors.As(err, &se) && se.RetryAfter > 0 {
		return se.RetryAfter
	}
	switch {
	case b.cur == 0:
		b.cur = time.Second
	case b.cur < 5*time.Minute:
		b.cur = min(b.cur*2, 5*time.Minute)
	}
	jitter := time.Duration(rand.Int64N(int64(b.cur/4) + 1)) //nolint:gosec // jitter, not security
	return b.cur - b.cur/8 + jitter
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
