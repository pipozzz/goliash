// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	autoMap, err := s.store.WorkspaceAutoMap(ctx, sc.WorkspaceID)
	if err != nil {
		return err
	}
	autoMapped := map[string]string{}
	changes, err := diff(diffInput{
		Target: target, Snapshot: payload, Existing: existing, Mapper: mapper, EnvByName: envByName,
		Baseline: !hasBase, AppLabel: appLabel, AutoMap: autoMap, AutoMapped: autoMapped,
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
	if err := s.store.SetWorkloadGitOps(ctx, sc, target.ID, gitOps(payload.Workloads), payload.Complete, payload.CollectedAt); err != nil {
		return err
	}
	// What was mapped automatically stays so through a rule per image, as the inbox would make it.
	repos := make([]string, 0, len(autoMapped))
	for repo := range autoMapped {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		if _, err := s.store.CreateMappingRule(ctx, store.MappingRule{
			Scope: sc, Priority: 100, MatchType: "image_repo", Pattern: mapping.ImagePattern(repo), ServiceID: autoMapped[repo],
		}); err != nil {
			return err
		}
		_ = s.store.Audit(ctx, store.AuditEntry{
			OrgID: sc.OrgID, WorkspaceID: sc.WorkspaceID, Actor: "auto-map", Action: "mapping.create",
			Details: map[string]string{"image_repo": mapping.ImagePattern(repo), "target": target.Name},
		})
		s.log.InfoContext(ctx, "image mapped to its suggested service", "repo", repo, "target", target.Name)
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

// gitOps are the GitOps applications a snapshot reports for its workloads: Argo CD and Flux as the collectors
// read them, or a Git repository a workload names in its labels (Nomad meta, Docker and Compose labels, ECS tags):
// goliash.git.repo, goliash.git.path (a directory or a file) and goliash.git.branch, so that upgrades open as
// pull requests without a GitOps tool.
func gitOps(ws []agentproto.Workload) []store.WorkloadGitOps {
	var out []store.WorkloadGitOps
	for _, w := range ws {
		g := w.Gitops
		if g == nil && w.Labels["goliash.git.repo"] != "" {
			out = append(out, store.WorkloadGitOps{
				WorkloadID: w.ID, Tool: "git", App: w.Name, Repo: w.Labels["goliash.git.repo"],
				Path: w.Labels["goliash.git.path"], TargetRevision: w.Labels["goliash.git.branch"],
			})
			continue
		}
		if g == nil || g.App == "" {
			continue
		}
		str := func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		}
		row := store.WorkloadGitOps{
			WorkloadID: w.ID, Tool: string(g.Tool), App: g.App, AppNamespace: str(g.AppNamespace), Project: str(g.Project),
			Repo: str(g.Repo), Path: str(g.Path), TargetRevision: str(g.TargetRevision), Revision: str(g.Revision),
			Sync: str(g.Sync), Health: str(g.Health), SyncBlocked: str(g.SyncBlocked),
			ManualSync: g.AutoSync != nil && !*g.AutoSync,
		}
		if g.SyncBlockedUntil != nil {
			row.SyncBlockedUntil = g.SyncBlockedUntil.UTC()
		}
		out = append(out, row)
	}
	return out
}
