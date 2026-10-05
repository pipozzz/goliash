// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Protocol defaults sent to agents.
const (
	HeartbeatInterval     = time.Minute
	RegistryCheckInterval = time.Hour
	StaleAfter            = 10 * time.Minute
	kubernetesDebounce    = 30
)

// ErrUnknownTarget means the agent sent data for a target that is not assigned to it.
var ErrUnknownTarget = errors.New("target is not assigned to this agent")

// Service handles what agents send. It is safe for concurrent use.
type Service struct {
	store     *store.Store
	log       *slog.Logger
	pending   chan struct{} // wakes the processor after a snapshot arrives
	onArrive  func()
	onEvents  func(store.Scope, []store.Event)
	upstreams Upstreams
}

// Upstreams is the version checker as seen by ingest: which private repositories
// agents should check, and where their results go.
type Upstreams interface {
	PrivateRepositories(ctx context.Context, sc store.Scope) ([]agentproto.RegistryCheck, error)
	RecordPrivateTags(ctx context.Context, sc store.Scope, repo string, tags []string, checkErr string) error
	RecordDigestMatches(ctx context.Context, sc store.Scope, repo string, matches map[string]string) error
}

// SetUpstreams connects the version checker. It must be called before serving.
func (s *Service) SetUpstreams(u Upstreams) { s.upstreams = u }

// New returns a Service backed by st.
func New(st *store.Store, log *slog.Logger) *Service {
	return &Service{store: st, log: log, pending: make(chan struct{}, 1)}
}

// Register records an agent's version, hostname and platforms.
func (s *Service) Register(ctx context.Context, a store.Agent, req agentproto.RegisterRequest) (agentproto.RegisterResponse, error) {
	platforms := make([]string, len(req.Platforms))
	for i, p := range req.Platforms {
		platforms[i] = string(p)
	}
	if err := s.store.RegisterAgent(ctx, a.Scope, a.ID, req.Version, req.Hostname, platforms); err != nil {
		return agentproto.RegisterResponse{}, err
	}
	s.log.InfoContext(ctx, "agent registered", "agent", a.Name, "agent_id", a.ID, "version", req.Version,
		"hostname", req.Hostname, "platforms", platforms)
	return agentproto.RegisterResponse{
		AgentID:     a.ID,
		WorkspaceID: a.Scope.WorkspaceID,
		ServerTime:  time.Now().UTC(),
	}, nil
}

// Config returns the agent's configuration and its ETag.
func (s *Service) Config(ctx context.Context, a store.Agent) (agentproto.AgentConfig, string, error) {
	targets, err := s.store.ListAgentTargets(ctx, a.Scope, a.ID)
	if err != nil {
		return agentproto.AgentConfig{}, "", err
	}
	cfg := agentproto.AgentConfig{
		HeartbeatIntervalSeconds: int(HeartbeatInterval / time.Second),
		Targets:                  make([]agentproto.Target, 0, len(targets)),
	}
	if s.upstreams != nil {
		if cfg.Registries, err = s.upstreams.PrivateRepositories(ctx, a.Scope); err != nil {
			return agentproto.AgentConfig{}, "", err
		}
		interval := int(RegistryCheckInterval / time.Second)
		cfg.RegistryCheckIntervalSeconds = &interval
	}
	for _, t := range targets {
		pt, err := ProtoTarget(t)
		if err != nil {
			return agentproto.AgentConfig{}, "", fmt.Errorf("target %s: %w", t.ID, err)
		}
		cfg.Targets = append(cfg.Targets, pt)
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return agentproto.AgentConfig{}, "", err
	}
	sum := sha256.Sum256(body)
	return cfg, `"` + hex.EncodeToString(sum[:16]) + `"`, nil
}

// ProtoTarget turns a stored target into its protocol form. Stored settings hold the
// platform object keyed by platform, e.g. {"kubernetes": {"exclude_namespaces": [...]}}.
func ProtoTarget(t store.Target) (agentproto.Target, error) {
	var pt agentproto.Target
	if err := json.Unmarshal(t.Settings, &pt); err != nil {
		return pt, fmt.Errorf("settings: %w", err)
	}
	pt.ID = t.ID
	pt.Name = t.Name
	pt.Platform = agentproto.Platform(t.Platform)
	pt.PollIntervalSeconds = t.PollIntervalSeconds
	if pt.Platform == agentproto.Kubernetes && pt.DebounceSeconds == nil {
		d := kubernetesDebounce
		pt.DebounceSeconds = &d
	}
	return pt, nil
}

// Snapshot stores a snapshot. raw is the decoded request body, kept as received.
// It reports false when the snapshot was already stored.
func (s *Service) Snapshot(ctx context.Context, a store.Agent, snap agentproto.Snapshot, raw []byte) (bool, error) {
	t, err := s.store.GetTarget(ctx, a.Scope, snap.TargetID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && t.AgentID != a.ID) {
		return false, ErrUnknownTarget
	}
	if err != nil {
		return false, err
	}
	inserted, err := s.store.InsertSnapshot(ctx, store.Snapshot{
		ID:          snap.SnapshotID,
		Scope:       a.Scope,
		TargetID:    t.ID,
		AgentID:     a.ID,
		CollectedAt: snap.CollectedAt,
		Complete:    snap.Complete,
		Payload:     raw,
	})
	if err != nil {
		return false, err
	}
	if inserted {
		s.arrived()
	}
	s.log.DebugContext(ctx, "snapshot received", "agent_id", a.ID, "target", t.Name, "snapshot_id", snap.SnapshotID,
		"workloads", len(snap.Workloads), "complete", snap.Complete, "duplicate", !inserted)
	return inserted, nil
}

// Heartbeat records that the agent is alive and stores collector health per target.
// Statuses for targets not assigned to the agent are ignored.
func (s *Service) Heartbeat(ctx context.Context, a store.Agent, hb agentproto.Heartbeat) (agentproto.HeartbeatResponse, error) {
	wasStale, err := s.store.TouchAgent(ctx, a.Scope, a.ID)
	if err != nil {
		return agentproto.HeartbeatResponse{}, err
	}
	if wasStale {
		s.log.InfoContext(ctx, "agent is back", "agent", a.Name, "agent_id", a.ID)
	}
	for _, c := range hb.Collectors {
		lastError := ""
		if c.LastError != nil {
			lastError = *c.LastError
		}
		err := s.store.ReportCollectorStatus(ctx, a.Scope, a.ID, c.TargetID, string(c.Status), lastError)
		if errors.Is(err, store.ErrNotFound) {
			s.log.WarnContext(ctx, "heartbeat for unassigned target", "agent_id", a.ID, "target_id", c.TargetID)
			continue
		}
		if err != nil {
			return agentproto.HeartbeatResponse{}, err
		}
	}
	_, etag, err := s.Config(ctx, a)
	if err != nil {
		return agentproto.HeartbeatResponse{}, err
	}
	return agentproto.HeartbeatResponse{ConfigEtag: etag}, nil
}

// RegistryResults records tags an agent found in private registries as releases.
func (s *Service) RegistryResults(ctx context.Context, a store.Agent, res agentproto.RegistryResults) error {
	s.log.DebugContext(ctx, "registry results received", "agent_id", a.ID, "repositories", len(res.Results))
	if s.upstreams == nil {
		return nil
	}
	for _, r := range res.Results {
		var tags []string
		for _, t := range r.Tags {
			tags = append(tags, t.Name)
		}
		checkErr := ""
		if r.Error != nil {
			checkErr = *r.Error
		}
		// Exact versions behind moving tags first: they are the reference tags compare with.
		if len(r.Resolved) > 0 {
			matches := map[string]string{}
			for _, m := range r.Resolved {
				matches[m.Digest] = ""
				if m.Tag != nil {
					matches[m.Digest] = *m.Tag
				}
			}
			if err := s.upstreams.RecordDigestMatches(ctx, a.Scope, r.Repository, matches); err != nil {
				return err
			}
		}
		if err := s.upstreams.RecordPrivateTags(ctx, a.Scope, r.Repository, tags, checkErr); err != nil {
			return err
		}
	}
	return nil
}

// Wake makes the processor look for new snapshots now, e.g. when another server
// received one.
func (s *Service) Wake() {
	select {
	case s.pending <- struct{}{}:
	default:
	}
}

// OnArrive registers a callback for every new snapshot, e.g. to tell the server that
// processes them when it is another one.
func (s *Service) OnArrive(fn func()) { s.onArrive = fn }

func (s *Service) arrived() {
	s.Wake()
	if s.onArrive != nil {
		s.onArrive()
	}
}

// WatchStale marks agents without a heartbeat for StaleAfter as stale, checking every
// interval until ctx ends. onStale is called for each agent that just became stale.
func (s *Service) WatchStale(ctx context.Context, interval time.Duration, onStale func(store.Agent)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.checkStale(ctx, onStale)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) checkStale(ctx context.Context, onStale func(store.Agent)) {
	stale, err := s.store.MarkStaleAgents(ctx, time.Now().Add(-StaleAfter))
	if err != nil {
		if ctx.Err() == nil {
			s.log.ErrorContext(ctx, "stale agent check failed", "err", err)
		}
		return
	}
	for _, a := range stale {
		s.log.WarnContext(ctx, "agent is stale", "agent", a.Name, "agent_id", a.ID, "last_seen_at", a.LastSeenAt)
		if onStale != nil {
			onStale(a)
		}
	}
}
