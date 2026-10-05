// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Instance is one observed container image (and digest) of a workload on a target.
// A new version is a new row; the old one is marked removed, which keeps history.
type Instance struct {
	ID               string
	Scope            Scope
	TargetID         string
	EnvironmentID    string
	ServiceID        string // empty while unmapped (the inbox)
	SuggestedService string
	WorkloadID       string
	WorkloadKind     string
	Namespace        string
	WorkloadName     string
	ContainerName    string
	Image            string
	Tag              string
	Digest           string
	Running          int
	IsMain           bool
	FirstSeenAt      time.Time
	LastSeenAt       time.Time
	RemovedAt        time.Time // zero while present
	App              string    // the application, from labels or the namespace
	AppSource        string    // the label App came from, or "namespace"
}

// Active reports whether the instance was present in the latest snapshot.
func (i Instance) Active() bool { return i.RemovedAt.IsZero() }

// Key identifies the same observed container across snapshots.
func (i Instance) Key() string {
	return strings.Join([]string{i.WorkloadID, i.ContainerName, i.Image, i.Digest}, "\x00")
}

// Event is one change in history.
type Event struct {
	ID            string
	Scope         Scope
	Type          string // deployed, version_changed, removed, new_release, drift_detected, drift_resolved
	ServiceID     string
	App           string // drift events of a service compared per application
	EnvironmentID string
	TargetID      string
	InstanceID    string
	FromVersion   string
	ToVersion     string
	Actor         string
	Source        string // poll, ci or git
	Note          string
	At            time.Time
}

// SnapshotChanges is everything one processed snapshot changes, applied atomically.
type SnapshotChanges struct {
	Scope      Scope
	SnapshotID string
	TargetID   string
	Upsert     []Instance // existing rows keep their ID; new rows need one
	Remove     []string   // instance IDs no longer observed
	Events     []Event
	At         time.Time // when the snapshot was collected
}

// ListTargetInstances returns every instance ever seen on a target, present or removed.
func (s *Store) ListTargetInstances(ctx context.Context, sc Scope, targetID string) ([]Instance, error) {
	return s.listInstances(ctx, `WHERE org_id = ? AND workspace_id = ? AND target_id = ? ORDER BY workload_name, container_name, first_seen_at`,
		sc.OrgID, sc.WorkspaceID, targetID)
}

// ListActiveInstances returns the instances present in each target's latest snapshot.
func (s *Store) ListActiveInstances(ctx context.Context, sc Scope) ([]Instance, error) {
	return s.listInstances(ctx, `WHERE org_id = ? AND workspace_id = ? AND removed_at IS NULL ORDER BY workload_name, container_name`,
		sc.OrgID, sc.WorkspaceID)
}

// MapInstances assigns a service to the active instances of one workload
// (classifying an inbox item).
func (s *Store) MapInstances(ctx context.Context, sc Scope, targetID, workloadID, serviceID string) error {
	_, err := s.exec(ctx, s.db, `UPDATE instances SET service_id = ?
		WHERE org_id = ? AND workspace_id = ? AND target_id = ? AND workload_id = ? AND removed_at IS NULL`,
		serviceID, sc.OrgID, sc.WorkspaceID, targetID, workloadID)
	return err
}

// InstancesAt returns the instances that ran at a point in time, with the replicas
// they had then, as if they were active.
func (s *Store) InstancesAt(ctx context.Context, sc Scope, at time.Time) ([]Instance, error) {
	at = at.UTC()
	rows, err := s.query(ctx, s.db, `SELECT instance_id, running FROM instance_history
		WHERE org_id = ? AND workspace_id = ? AND since <= ? AND (until IS NULL OR until > ?)`,
		sc.OrgID, sc.WorkspaceID, at, at)
	if err != nil {
		return nil, err
	}
	running := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		running[id] = n
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	all, err := s.listInstances(ctx, `WHERE org_id = ? AND workspace_id = ? AND first_seen_at <= ?
		ORDER BY workload_name, container_name`, sc.OrgID, sc.WorkspaceID, at)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, i := range all {
		if n, ok := running[i.ID]; ok {
			i.Running, i.RemovedAt = n, time.Time{}
			out = append(out, i)
		}
	}
	return out, nil
}

func (s *Store) listInstances(ctx context.Context, where string, args ...any) ([]Instance, error) {
	rows, err := s.query(ctx, s.db, `SELECT `+instanceColumns+` FROM instances `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Instance
	for rows.Next() {
		var (
			i        Instance
			env, svc sql.NullString
			removed  sql.NullTime
		)
		if err := rows.Scan(&i.ID, &i.Scope.OrgID, &i.Scope.WorkspaceID, &i.TargetID, &env, &svc, &i.SuggestedService,
			&i.WorkloadID, &i.WorkloadKind, &i.Namespace, &i.WorkloadName, &i.ContainerName, &i.Image, &i.Tag, &i.Digest,
			&i.Running, &i.IsMain, &i.FirstSeenAt, &i.LastSeenAt, &removed, &i.App, &i.AppSource); err != nil {
			return nil, err
		}
		i.EnvironmentID, i.ServiceID, i.RemovedAt = env.String, svc.String, timeOrZero(removed)
		i.FirstSeenAt, i.LastSeenAt = i.FirstSeenAt.UTC(), i.LastSeenAt.UTC()
		out = append(out, i)
	}
	return out, rows.Err()
}

const instanceColumns = `id, org_id, workspace_id, target_id, environment_id, service_id, suggested_service,
	workload_id, workload_kind, namespace, workload_name, container_name, image, tag, digest, running, is_main,
	first_seen_at, last_seen_at, removed_at, app, app_source`

// ApplySnapshot writes the instances, removals and events derived from a snapshot
// and marks the snapshot processed, in one transaction.
func (s *Store) ApplySnapshot(ctx context.Context, ch SnapshotChanges) error {
	at := ch.At.UTC()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, i := range ch.Upsert {
			if i.ID == "" {
				i.ID = NewID()
			}
			_, err := s.exec(ctx, tx, `
				INSERT INTO instances (id, org_id, workspace_id, target_id, environment_id, service_id, suggested_service,
					workload_id, workload_kind, namespace, workload_name, container_name, image, tag, digest, running,
					is_main, first_seen_at, last_seen_at, removed_at, app, app_source)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
				ON CONFLICT (target_id, workload_id, container_name, image, digest) DO UPDATE SET
					environment_id = excluded.environment_id, service_id = excluded.service_id,
					suggested_service = excluded.suggested_service, workload_kind = excluded.workload_kind,
					namespace = excluded.namespace, workload_name = excluded.workload_name, tag = excluded.tag,
					running = excluded.running, is_main = excluded.is_main, last_seen_at = excluded.last_seen_at,
					removed_at = NULL, app = excluded.app, app_source = excluded.app_source`,
				i.ID, ch.Scope.OrgID, ch.Scope.WorkspaceID, ch.TargetID, nullString(i.EnvironmentID), nullString(i.ServiceID),
				i.SuggestedService, i.WorkloadID, i.WorkloadKind, i.Namespace, i.WorkloadName, i.ContainerName, i.Image,
				i.Tag, i.Digest, i.Running, i.IsMain, at, at, i.App, i.AppSource)
			if err != nil {
				return err
			}
			// Open a history period when the instance starts running (new, or back again).
			if _, err := s.exec(ctx, tx, `
				INSERT INTO instance_history (id, org_id, workspace_id, instance_id, running, since)
				SELECT ?, org_id, workspace_id, id, running, ? FROM instances
				WHERE target_id = ? AND workload_id = ? AND container_name = ? AND image = ? AND digest = ?
					AND NOT EXISTS (SELECT 1 FROM instance_history h WHERE h.instance_id = instances.id AND h.until IS NULL)`,
				NewID(), at, ch.TargetID, i.WorkloadID, i.ContainerName, i.Image, i.Digest); err != nil {
				return err
			}
		}
		for _, id := range ch.Remove {
			if _, err := s.exec(ctx, tx, `UPDATE instances SET removed_at = ?, running = 0
				WHERE id = ? AND org_id = ? AND workspace_id = ? AND removed_at IS NULL`,
				at, id, ch.Scope.OrgID, ch.Scope.WorkspaceID); err != nil {
				return err
			}
			if _, err := s.exec(ctx, tx, `UPDATE instance_history SET until = ?
				WHERE instance_id = ? AND org_id = ? AND workspace_id = ? AND until IS NULL`,
				at, id, ch.Scope.OrgID, ch.Scope.WorkspaceID); err != nil {
				return err
			}
		}
		for _, e := range ch.Events {
			if err := s.insertEvent(ctx, tx, ch.Scope, e); err != nil {
				return err
			}
		}
		_, err := s.exec(ctx, tx, `UPDATE snapshots SET processed_at = ? WHERE id = ? AND org_id = ? AND workspace_id = ?`,
			s.now(), ch.SnapshotID, ch.Scope.OrgID, ch.Scope.WorkspaceID)
		return err
	})
}

func (s *Store) insertEvent(ctx context.Context, q queryer, sc Scope, e Event) error {
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.Source == "" {
		e.Source = "poll"
	}
	_, err := s.exec(ctx, q, `INSERT INTO events (id, org_id, workspace_id, type, service_id, environment_id, target_id,
		instance_id, from_version, to_version, actor, source, note, at, app) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, sc.OrgID, sc.WorkspaceID, e.Type, nullString(e.ServiceID), nullString(e.EnvironmentID),
		nullString(e.TargetID), nullString(e.InstanceID), e.FromVersion, e.ToVersion, e.Actor, e.Source, e.Note, e.At.UTC(), e.App)
	return err
}

// InsertEvent records an event outside snapshot processing (releases, drift).
func (s *Store) InsertEvent(ctx context.Context, sc Scope, e Event) error {
	return s.insertEvent(ctx, s.db, sc, e)
}

// EventFilter narrows ListEvents. Zero values match everything.
type EventFilter struct {
	ServiceID     string
	EnvironmentID string
	Types         []string
	Before        time.Time // events strictly before this time, for paging
	Since         time.Time // events at or after this time ("what changed in the last hour")
	Limit         int       // default 100
}

// ListEvents returns events newest first.
func (s *Store) ListEvents(ctx context.Context, sc Scope, f EventFilter) ([]Event, error) {
	where := []string{"org_id = ?", "workspace_id = ?"}
	args := []any{sc.OrgID, sc.WorkspaceID}
	if f.ServiceID != "" {
		where, args = append(where, "service_id = ?"), append(args, f.ServiceID)
	}
	if f.EnvironmentID != "" {
		where, args = append(where, "environment_id = ?"), append(args, f.EnvironmentID)
	}
	if len(f.Types) > 0 {
		where = append(where, "type IN (?"+strings.Repeat(", ?", len(f.Types)-1)+")")
		for _, t := range f.Types {
			args = append(args, t)
		}
	}
	if !f.Since.IsZero() {
		where, args = append(where, "at >= ?"), append(args, f.Since.UTC())
	}
	if !f.Before.IsZero() {
		where, args = append(where, "at < ?"), append(args, f.Before.UTC())
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	args = append(args, f.Limit)

	rows, err := s.query(ctx, s.db, `SELECT id, type, service_id, environment_id, target_id, instance_id, from_version,
		to_version, actor, source, note, at, app FROM events WHERE `+strings.Join(where, " AND ")+`
		ORDER BY at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		e := Event{Scope: sc}
		var svc, env, tgt, inst sql.NullString
		if err := rows.Scan(&e.ID, &e.Type, &svc, &env, &tgt, &inst, &e.FromVersion, &e.ToVersion, &e.Actor, &e.Source,
			&e.Note, &e.At, &e.App); err != nil {
			return nil, err
		}
		e.ServiceID, e.EnvironmentID, e.TargetID, e.InstanceID = svc.String, env.String, tgt.String, inst.String
		e.At = e.At.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// HasProcessedSnapshot reports whether any snapshot of the target has been processed,
// i.e. whether there is a baseline to diff against.
func (s *Store) HasProcessedSnapshot(ctx context.Context, sc Scope, targetID string) (bool, error) {
	var n int
	err := s.queryRow(ctx, s.db, `SELECT COUNT(*) FROM snapshots
		WHERE org_id = ? AND workspace_id = ? AND target_id = ? AND processed_at IS NOT NULL`,
		sc.OrgID, sc.WorkspaceID, targetID).Scan(&n)
	return n > 0, err
}

// LatestProcessedAt returns the collected_at of the newest processed snapshot of a target.
func (s *Store) LatestProcessedAt(ctx context.Context, sc Scope, targetID string) (time.Time, error) {
	var t time.Time
	err := s.queryRow(ctx, s.db, `SELECT collected_at FROM snapshots
		WHERE org_id = ? AND workspace_id = ? AND target_id = ? AND processed_at IS NOT NULL
		ORDER BY collected_at DESC LIMIT 1`,
		sc.OrgID, sc.WorkspaceID, targetID).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	return t.UTC(), err
}
