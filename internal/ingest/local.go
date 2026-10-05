// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/pipozzz/goliash/internal/agent"
	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/pkg/agentproto"
	"github.com/pipozzz/goliash/pkg/buildinfo"
)

// LocalSnapshot stores a snapshot the server collected itself for a target without agent.
func (s *Service) LocalSnapshot(ctx context.Context, t store.Target, snap agentproto.Snapshot) (bool, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return false, err
	}
	inserted, err := s.store.InsertSnapshot(ctx, store.Snapshot{
		ID: snap.SnapshotID, Scope: t.Scope, TargetID: t.ID, CollectedAt: snap.CollectedAt, Complete: snap.Complete, Payload: raw,
	})
	if inserted {
		s.arrived()
	}
	return inserted, err
}

// ServerCollectors runs collectors in the server for targets that have no agent, so a
// self-hosted installation works without one (for example the cluster it runs in).
type ServerCollectors struct {
	svc       *Service
	store     *store.Store
	factories map[agentproto.Platform]collectors.Factory
	log       *slog.Logger

	mu      sync.Mutex
	running map[string]*serverTarget
}

type serverTarget struct {
	target store.Target
	spec   []byte
	runner *agent.Runner
	cancel context.CancelFunc
	done   chan struct{}
}

// NewServerCollectors returns a manager using the given collector factories.
func NewServerCollectors(svc *Service, st *store.Store, factories map[agentproto.Platform]collectors.Factory, log *slog.Logger) *ServerCollectors {
	return &ServerCollectors{svc: svc, store: st, factories: factories, log: log, running: map[string]*serverTarget{}}
}

// Run follows the targets without agent every interval, and reports collector
// health, until ctx ends.
func (m *ServerCollectors) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer m.stopAll()
	for {
		if err := m.reconcile(ctx); err != nil && ctx.Err() == nil {
			m.log.ErrorContext(ctx, "server collectors", "err", err)
		}
		m.report(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *ServerCollectors) reconcile(ctx context.Context) error {
	targets, err := m.store.ListServerTargets(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[string]store.Target{}
	for _, t := range targets {
		want[t.ID] = t
	}
	for id, rt := range m.running {
		t, keep := want[id]
		if keep && bytes.Equal(rt.spec, targetSpec(t)) {
			continue
		}
		rt.cancel()
		<-rt.done
		delete(m.running, id)
	}
	for id, t := range want {
		if _, ok := m.running[id]; ok {
			continue
		}
		m.running[id] = m.start(ctx, t)
	}
	return nil
}

func targetSpec(t store.Target) []byte {
	b, _ := json.Marshal([]any{t.Platform, t.Name, string(t.Settings), t.PollIntervalSeconds})
	return b
}

func (m *ServerCollectors) start(ctx context.Context, t store.Target) *serverTarget {
	log := m.log.With("target", t.Name, "collected_by", "server")
	pt, err := ProtoTarget(t)
	var r *agent.Runner
	switch factory, ok := m.factories[agentproto.Platform(t.Platform)]; {
	case err != nil:
		r = agent.FailingRunner(pt, "invalid target settings: "+err.Error())
	case !ok:
		r = agent.FailingRunner(pt, "the server has no "+t.Platform+" collector")
	default:
		c, err := factory(ctx, pt)
		if err != nil {
			r = agent.FailingRunner(pt, err.Error())
			log.WarnContext(ctx, "collector setup failed", "err", err)
		} else {
			r = agent.NewRunner(pt, c, func(_ agentproto.Target, res collectors.Result) (string, error) {
				return m.emit(ctx, t, res)
			}, log)
			log.InfoContext(ctx, "server collects target", "platform", t.Platform)
		}
	}
	rctx, cancel := context.WithCancel(ctx)
	st := &serverTarget{target: t, spec: targetSpec(t), runner: r, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(st.done)
		r.Run(rctx)
	}()
	return st
}

func (m *ServerCollectors) emit(ctx context.Context, t store.Target, res collectors.Result) (string, error) {
	version := buildinfo.Version
	snap := agentproto.Snapshot{
		SnapshotID: ulid.Make().String(), TargetID: t.ID, CollectedAt: time.Now().UTC(), AgentVersion: &version,
		Complete: res.Complete, Errors: res.Errors, Workloads: res.Workloads,
	}
	if snap.Workloads == nil {
		snap.Workloads = []agentproto.Workload{}
	}
	_, err := m.svc.LocalSnapshot(ctx, t, snap)
	return snap.SnapshotID, err
}

func (m *ServerCollectors) report(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rt := range m.running {
		st := rt.runner.Status()
		msg := ""
		if st.LastError != nil {
			msg = *st.LastError
		}
		if err := m.store.ReportServerCollectorStatus(ctx, rt.target.Scope, rt.target.ID, string(st.Status), msg); err != nil && ctx.Err() == nil {
			m.log.WarnContext(ctx, "collector status not stored", "target", rt.target.Name, "err", err)
		}
	}
}

func (m *ServerCollectors) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, rt := range m.running {
		rt.cancel()
		<-rt.done
		delete(m.running, id)
	}
}
