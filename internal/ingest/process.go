// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pipozzz/goliash/internal/mapping"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// processBatch is how many snapshots one ProcessPending pass handles.
const processBatch = 50

// ProcessPending diffs stored snapshots into instances and events, oldest first,
// and returns how many it processed.
func (s *Service) ProcessPending(ctx context.Context) (int, error) {
	total := 0
	for {
		snaps, err := s.store.UnprocessedSnapshots(ctx, processBatch)
		if err != nil {
			return total, err
		}
		for _, snap := range snaps {
			if err := s.process(ctx, snap); err != nil {
				return total, fmt.Errorf("snapshot %s: %w", snap.ID, err)
			}
			total++
		}
		if len(snaps) < processBatch {
			return total, nil
		}
	}
}

func (s *Service) process(ctx context.Context, snap store.Snapshot) error {
	sc := snap.Scope
	target, err := s.store.GetTarget(ctx, sc, snap.TargetID)
	if err != nil {
		return err
	}

	// A snapshot collected before one already processed is out of date: it would
	// undo newer state. Record it as processed without changes.
	latest, err := s.store.LatestProcessedAt(ctx, sc, target.ID)
	if err != nil {
		return err
	}
	if !latest.IsZero() && !snap.CollectedAt.After(latest) {
		s.log.DebugContext(ctx, "skipping out-of-date snapshot", "snapshot_id", snap.ID, "target", target.Name)
		return s.store.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, SnapshotID: snap.ID, TargetID: target.ID, At: snap.CollectedAt})
	}

	var payload agentproto.Snapshot
	if err := json.Unmarshal(snap.Payload, &payload); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	payload.CollectedAt = snap.CollectedAt

	hasBase, err := s.store.HasProcessedSnapshot(ctx, sc, target.ID)
	if err != nil {
		return err
	}
	rules, err := s.store.ListMappingRules(ctx, sc)
	if err != nil {
		return err
	}
	mapper, ruleErrs := mapping.New(rules)
	for _, e := range ruleErrs {
		s.log.WarnContext(ctx, "mapping rule skipped", "err", e)
	}
	envs, err := s.store.ListEnvironments(ctx, sc)
	if err != nil {
		return err
	}
	envByName := map[string]string{}
	for _, e := range envs {
		envByName[e.Name] = e.ID
	}
	existing, err := s.store.ListTargetInstances(ctx, sc, target.ID)
	if err != nil {
		return err
	}

	serviceIDs := map[string]string{}
	appLabel, err := s.store.WorkspaceAppLabel(ctx, sc.WorkspaceID)
	if err != nil {
		return err
	}
	changes, err := diff(diffInput{
		Target: target, Snapshot: payload, Existing: existing, Mapper: mapper, EnvByName: envByName,
		Baseline: !hasBase, AppLabel: appLabel,
		ServiceID: func(name string) (string, error) {
			if id, ok := serviceIDs[name]; ok {
				return id, nil
			}
			svc, err := s.store.EnsureService(ctx, sc, name)
			if err != nil {
				return "", err
			}
			serviceIDs[name] = svc.ID
			return svc.ID, nil
		},
	})
	if err != nil {
		return err
	}
	if err := s.store.ApplySnapshot(ctx, changes); err != nil {
		return err
	}
	s.log.DebugContext(ctx, "snapshot processed", "snapshot_id", snap.ID, "target", target.Name,
		"instances", len(changes.Upsert), "removed", len(changes.Remove), "events", len(changes.Events), "baseline", !hasBase)
	for _, e := range changes.Events {
		s.log.InfoContext(ctx, "event", "type", e.Type, "target", target.Name, "from", e.FromVersion, "to", e.ToVersion, "note", e.Note)
	}
	if s.onEvents != nil && len(changes.Events) > 0 {
		s.onEvents(sc, changes.Events)
	}
	return nil
}

// RunProcessor processes snapshots as they arrive (and every interval as a fallback)
// until ctx ends.
func (s *Service) RunProcessor(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := s.ProcessPending(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "snapshot processing failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.pending:
		}
	}
}

// OnEvents registers a callback for events produced by snapshot processing
// (notifications). It must be set before RunProcessor starts.
func (s *Service) OnEvents(fn func(store.Scope, []store.Event)) { s.onEvents = fn }
