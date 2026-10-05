// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// forEachDialect runs fn against a fresh SQLite database and, when
// GOLIASH_TEST_POSTGRES_DSN is set, against a fresh PostgreSQL schema.
func forEachDialect(t *testing.T, fn func(t *testing.T, s *Store)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		s, err := Open(context.Background(), "sqlite://:memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		fn(t, s)
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("GOLIASH_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("GOLIASH_TEST_POSTGRES_DSN not set")
		}
		fn(t, openPostgresSchema(t, dsn))
	})
}

// openPostgresSchema creates an isolated schema for one test and drops it afterwards.
func openPostgresSchema(t *testing.T, dsn string) *Store {
	t.Helper()
	ctx := context.Background()
	schema := "test_" + strings.ToLower(NewID())

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE") })

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	s, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

type fixture struct {
	ws   Workspace
	env  Environment
	agnt Agent
	tgt  Target
}

func setup(t *testing.T, s *Store) fixture {
	t.Helper()
	ctx := context.Background()
	ws, err := s.EnsureDefaultWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	env, err := s.CreateEnvironment(ctx, ws.Scope(), "prod", 30)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.CreateAgent(ctx, ws.Scope(), "eu-cluster", "hash-"+NewID())
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := s.CreateTarget(ctx, Target{
		Scope: ws.Scope(), EnvironmentID: env.ID, AgentID: agent.ID, Platform: "kubernetes", Name: "prod-eu-1",
		Settings: json.RawMessage(`{"kubernetes":{"exclude_namespaces":["kube-system"]}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{ws: ws, env: env, agnt: agent, tgt: tgt}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := t.TempDir() + "/goliash.db"
	for range 2 {
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if s.Dialect() != SQLite {
			t.Fatalf("dialect = %s", s.Dialect())
		}
		_ = s.Close()
	}
}

func TestEnsureDefaultWorkspace(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		first, err := s.EnsureDefaultWorkspace(ctx)
		if err != nil {
			t.Fatal(err)
		}
		again, err := s.EnsureDefaultWorkspace(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if first.ID != again.ID || first.OrgID != again.OrgID {
			t.Fatalf("second call created another workspace: %+v vs %+v", first, again)
		}
		if first.Slug != "default" {
			t.Fatalf("slug = %q", first.Slug)
		}
	})
}

func TestEnvironmentsInPromotionOrder(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, err := s.EnsureDefaultWorkspace(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range []struct {
			name string
			pos  int
		}{{"prod", 30}, {"dev", 10}, {"staging", 20}} {
			if _, err := s.CreateEnvironment(ctx, ws.Scope(), e.name, e.pos); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.CreateEnvironment(ctx, ws.Scope(), "prod", 40); err == nil {
			t.Fatal("duplicate environment name was accepted")
		}

		envs, err := s.ListEnvironments(ctx, ws.Scope())
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range envs {
			names = append(names, e.Name)
		}
		if got := strings.Join(names, ","); got != "dev,staging,prod" {
			t.Fatalf("order = %s", got)
		}
		if env, err := s.GetEnvironmentByName(ctx, ws.Scope(), "staging"); err != nil || env.Position != 20 {
			t.Fatalf("by name: %+v %v", env, err)
		}
		if _, err := s.GetEnvironmentByName(ctx, ws.Scope(), "qa"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing env: %v", err)
		}
	})
}

func TestAgentTokenLifecycle(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, err := s.EnsureDefaultWorkspace(ctx)
		if err != nil {
			t.Fatal(err)
		}
		created, err := s.CreateAgent(ctx, ws.Scope(), "eu-cluster", "hash-1")
		if err != nil {
			t.Fatal(err)
		}

		got, err := s.AgentByTokenHash(ctx, "hash-1")
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != created.ID || got.Scope != ws.Scope() || !got.RegisteredAt.IsZero() {
			t.Fatalf("unexpected agent %+v", got)
		}

		if err := s.RegisterAgent(ctx, ws.Scope(), created.ID, "0.1.0", "node-1", []string{"kubernetes", "ecs"}); err != nil {
			t.Fatal(err)
		}
		got, err = s.GetAgent(ctx, ws.Scope(), created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if byName, err := s.GetAgentByName(ctx, ws.Scope(), "eu-cluster"); err != nil || byName.ID != created.ID {
			t.Fatalf("by name: %+v %v", byName, err)
		}
		if got.Version != "0.1.0" || got.Hostname != "node-1" || strings.Join(got.Platforms, ",") != "kubernetes,ecs" {
			t.Fatalf("register not stored: %+v", got)
		}
		if got.RegisteredAt.IsZero() || got.LastSeenAt.IsZero() {
			t.Fatal("register did not set timestamps")
		}

		if err := s.RevokeAgentTokens(ctx, ws.Scope(), created.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AgentByTokenHash(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("revoked token still authenticates: %v", err)
		}
		if _, err := s.AgentByTokenHash(ctx, "unknown"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown token: %v", err)
		}
	})
}

func TestStaleAgents(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		never, err := s.CreateAgent(ctx, f.ws.Scope(), "never-connected", "hash-never")
		if err != nil {
			t.Fatal(err)
		}

		clock := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
		s.now = func() time.Time { return clock }
		if wasStale, err := s.TouchAgent(ctx, f.ws.Scope(), f.agnt.ID); err != nil || wasStale {
			t.Fatalf("touch: wasStale=%v err=%v", wasStale, err)
		}

		clock = clock.Add(11 * time.Minute)
		stale, err := s.MarkStaleAgents(ctx, clock.Add(-10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if len(stale) != 1 || stale[0].ID != f.agnt.ID || !stale[0].StaleSince.Equal(clock) {
			t.Fatalf("stale = %+v (never-connected agent %s must be ignored)", stale, never.ID)
		}
		if again, err := s.MarkStaleAgents(ctx, clock); err != nil || len(again) != 0 {
			t.Fatalf("stale agent reported twice: %+v %v", again, err)
		}

		if wasStale, err := s.TouchAgent(ctx, f.ws.Scope(), f.agnt.ID); err != nil || !wasStale {
			t.Fatalf("touch after stale: wasStale=%v err=%v", wasStale, err)
		}
		got, err := s.GetAgent(ctx, f.ws.Scope(), f.agnt.ID)
		if err != nil || !got.StaleSince.IsZero() || !got.LastSeenAt.Equal(clock) {
			t.Fatalf("after touch: %+v %v", got, err)
		}
	})
}

func TestReportCollectorStatus(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		if err := s.ReportCollectorStatus(ctx, f.ws.Scope(), f.agnt.ID, f.tgt.ID, "degraded", "forbidden"); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetTarget(ctx, f.ws.Scope(), f.tgt.ID)
		if err != nil || got.CollectorStatus != "degraded" || got.CollectorError != "forbidden" || got.CollectorReportedAt.IsZero() {
			t.Fatalf("target = %+v, %v", got, err)
		}
		other, err := s.CreateAgent(ctx, f.ws.Scope(), "other", "hash-other")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ReportCollectorStatus(ctx, f.ws.Scope(), other.ID, f.tgt.ID, "ok", ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("agent reported status for a target it does not own: %v", err)
		}
	})
}

func TestWorkspaceIsolation(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		other, err := s.CreateWorkspace(ctx, f.ws.OrgID, "Client B", "client-b")
		if err != nil {
			t.Fatal(err)
		}

		if _, err := s.GetTarget(ctx, other.Scope(), f.tgt.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("target visible from another workspace: %v", err)
		}
		if _, err := s.GetAgent(ctx, other.Scope(), f.agnt.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("agent visible from another workspace: %v", err)
		}
		if err := s.RegisterAgent(ctx, other.Scope(), f.agnt.ID, "x", "x", nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("agent updated from another workspace: %v", err)
		}
		targets, err := s.ListAgentTargets(ctx, other.Scope(), f.agnt.ID)
		if err != nil || len(targets) != 0 {
			t.Fatalf("targets from another workspace: %v %v", targets, err)
		}
	})
}

func TestTargets(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)

		got, err := s.GetTarget(ctx, f.ws.Scope(), f.tgt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.AgentID != f.agnt.ID || got.PollIntervalSeconds != 300 || got.Platform != "kubernetes" {
			t.Fatalf("unexpected target %+v", got)
		}
		var settings map[string]any
		if err := json.Unmarshal(got.Settings, &settings); err != nil || settings["kubernetes"] == nil {
			t.Fatalf("settings not round-tripped: %s (%v)", got.Settings, err)
		}

		serverSide, err := s.CreateTarget(ctx, Target{
			Scope: f.ws.Scope(), EnvironmentID: f.env.ID, Platform: "swarm", Name: "swarm-local",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetTarget(ctx, f.ws.Scope(), serverSide.ID); got.AgentID != "" {
			t.Fatalf("server-side target has agent %q", got.AgentID)
		}

		if _, err := s.CreateTarget(ctx, Target{
			Scope: f.ws.Scope(), EnvironmentID: f.env.ID, Platform: "compose", Name: "compose-files",
		}); err != nil {
			t.Fatalf("docker platform: %v", err)
		}

		if _, err := s.CreateTarget(ctx, Target{
			Scope: f.ws.Scope(), EnvironmentID: f.env.ID, Platform: "openshift", Name: "x",
		}); err == nil {
			t.Fatal("unknown platform was accepted")
		}

		list, err := s.ListAgentTargets(ctx, f.ws.Scope(), f.agnt.ID)
		if err != nil || len(list) != 1 || list[0].ID != f.tgt.ID {
			t.Fatalf("agent targets = %+v, %v", list, err)
		}
	})
}

func TestSnapshots(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		base := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)

		snap := func(id string, at time.Time) Snapshot {
			return Snapshot{
				ID: id, Scope: f.ws.Scope(), TargetID: f.tgt.ID, AgentID: f.agnt.ID, CollectedAt: at,
				Complete: true, Payload: json.RawMessage(`{"workloads":[]}`),
			}
		}
		first, second := snap(NewID(), base), snap(NewID(), base.Add(5*time.Minute+123*time.Millisecond))

		for _, sn := range []Snapshot{second, first} { // out of order on purpose
			ok, err := s.InsertSnapshot(ctx, sn)
			if err != nil || !ok {
				t.Fatalf("insert %s: %v %v", sn.ID, ok, err)
			}
		}
		if ok, err := s.InsertSnapshot(ctx, first); err != nil || ok {
			t.Fatalf("duplicate insert: inserted=%v err=%v", ok, err)
		}

		tgt, err := s.GetTarget(ctx, f.ws.Scope(), f.tgt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !tgt.LastSnapshotAt.Equal(second.CollectedAt) {
			t.Fatalf("last_snapshot_at = %v, want %v (an older snapshot must not move it back)",
				tgt.LastSnapshotAt, second.CollectedAt)
		}

		pending, err := s.UnprocessedSnapshots(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) != 2 || pending[0].ID != first.ID || pending[1].ID != second.ID {
			t.Fatalf("unprocessed order wrong: %+v", pending)
		}
		if !pending[1].CollectedAt.Equal(second.CollectedAt) || !pending[0].Complete {
			t.Fatalf("snapshot fields not round-tripped: %+v", pending[1])
		}

		if _, err := s.PreviousSnapshot(ctx, pending[0]); !errors.Is(err, ErrNotFound) {
			t.Fatalf("first snapshot has a predecessor: %v", err)
		}
		if err := s.MarkSnapshotProcessed(ctx, f.ws.Scope(), first.ID); err != nil {
			t.Fatal(err)
		}
		prev, err := s.PreviousSnapshot(ctx, pending[1])
		if err != nil {
			t.Fatal(err)
		}
		if prev.ID != first.ID || prev.ProcessedAt.IsZero() || string(prev.Payload) == "" {
			t.Fatalf("previous = %+v", prev)
		}

		pending, err = s.UnprocessedSnapshots(ctx, 10)
		if err != nil || len(pending) != 1 || pending[0].ID != second.ID {
			t.Fatalf("after processing: %+v %v", pending, err)
		}
	})
}

func TestRebind(t *testing.T) {
	pg := &Store{dialect: Postgres}
	if got := pg.rebind("a = ? AND b IN (?, ?)"); got != "a = $1 AND b IN ($2, $3)" {
		t.Fatalf("postgres rebind = %q", got)
	}
	lite := &Store{dialect: SQLite}
	if got := lite.rebind("a = ?"); got != "a = ?" {
		t.Fatalf("sqlite rebind = %q", got)
	}
}

// The two migration sets are written by hand; this keeps them in step.
func TestSchemaParity(t *testing.T) {
	dsn := os.Getenv("GOLIASH_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOLIASH_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	lite, err := Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lite.Close() })
	pg := openPostgresSchema(t, dsn)

	liteCols := columns(t, lite, `
		SELECT m.name || '.' || p.name FROM sqlite_master m, pragma_table_info(m.name) p
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' AND m.name <> 'goose_db_version'`)
	pgCols := columns(t, pg, `
		SELECT table_name || '.' || column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name <> 'goose_db_version'`)

	for c := range liteCols {
		if !pgCols[c] {
			t.Errorf("only in SQLite: %s", c)
		}
	}
	for c := range pgCols {
		if !liteCols[c] {
			t.Errorf("only in PostgreSQL: %s", c)
		}
	}
	if len(liteCols) == 0 {
		t.Fatal("no columns found")
	}
}

func columns(t *testing.T, s *Store, query string) map[string]bool {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols[c] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cols
}

func TestApplySnapshotAndEvents(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		svc, err := s.EnsureService(ctx, sc, "payments")
		if err != nil {
			t.Fatal(err)
		}
		if again, err := s.EnsureService(ctx, sc, "payments"); err != nil || again.ID != svc.ID {
			t.Fatalf("EnsureService not idempotent: %v %v", again, err)
		}
		at := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
		snap := func(id string, at time.Time) {
			if _, err := s.InsertSnapshot(ctx, Snapshot{
				ID: id, Scope: sc, TargetID: f.tgt.ID, CollectedAt: at,
				Complete: true, Payload: json.RawMessage(`{}`),
			}); err != nil {
				t.Fatal(err)
			}
		}

		first := Instance{
			ID: NewID(), EnvironmentID: f.env.ID, ServiceID: svc.ID, WorkloadID: "w1", WorkloadKind: "deployment",
			WorkloadName: "payments-api", ContainerName: "app", Image: "ghcr.io/acme/payments-api:1.4.2", Tag: "1.4.2",
			Running: 3, IsMain: true, App: "shop", AppSource: "app.kubernetes.io/part-of",
		}
		s1 := NewID()
		snap(s1, at)
		if err := s.ApplySnapshot(ctx, SnapshotChanges{Scope: sc, SnapshotID: s1, TargetID: f.tgt.ID, At: at, Upsert: []Instance{first}}); err != nil {
			t.Fatal(err)
		}

		// Same key again with a new running count updates in place (ON CONFLICT), a new version is a new row.
		first.Running = 1
		second := first
		second.ID, second.Image, second.Tag = NewID(), "ghcr.io/acme/payments-api:1.5.0", "1.5.0"
		second.Running = 2
		s2 := NewID()
		snap(s2, at.Add(time.Minute))
		if err := s.ApplySnapshot(ctx, SnapshotChanges{
			Scope: sc, SnapshotID: s2, TargetID: f.tgt.ID, At: at.Add(time.Minute),
			Upsert: []Instance{first, second},
			Events: []Event{{
				Type: "version_changed", ServiceID: svc.ID, EnvironmentID: f.env.ID, TargetID: f.tgt.ID,
				InstanceID: second.ID, FromVersion: "1.4.2", ToVersion: "1.5.0", At: at.Add(time.Minute),
			}},
		}); err != nil {
			t.Fatal(err)
		}
		s3 := NewID()
		snap(s3, at.Add(2*time.Minute))
		if err := s.ApplySnapshot(ctx, SnapshotChanges{
			Scope: sc, SnapshotID: s3, TargetID: f.tgt.ID, At: at.Add(2 * time.Minute),
			Upsert: []Instance{second}, Remove: []string{first.ID},
		}); err != nil {
			t.Fatal(err)
		}

		all, err := s.ListTargetInstances(ctx, sc, f.tgt.ID)
		if err != nil || len(all) != 2 {
			t.Fatalf("instances %+v %v", all, err)
		}
		active, err := s.ListActiveInstances(ctx, sc)
		if err != nil || len(active) != 1 || active[0].Tag != "1.5.0" || active[0].Running != 2 || !active[0].IsMain ||
			active[0].App != "shop" || active[0].AppSource != "app.kubernetes.io/part-of" {
			t.Fatalf("active %+v %v", active, err)
		}
		if !active[0].FirstSeenAt.Equal(at.Add(time.Minute)) || !active[0].LastSeenAt.Equal(at.Add(2*time.Minute)) {
			t.Fatalf("seen times %v %v", active[0].FirstSeenAt, active[0].LastSeenAt)
		}
		if latest, err := s.LatestProcessedAt(ctx, sc, f.tgt.ID); err != nil || !latest.Equal(at.Add(2*time.Minute)) {
			t.Fatalf("latest processed %v %v", latest, err)
		}
		if pending, _ := s.UnprocessedSnapshots(ctx, 10); len(pending) != 0 {
			t.Fatal("snapshots not marked processed")
		}

		evs, err := s.ListEvents(ctx, sc, EventFilter{ServiceID: svc.ID, Types: []string{"version_changed", "deployed"}})
		if err != nil || len(evs) != 1 || evs[0].ToVersion != "1.5.0" || evs[0].Source != "poll" || evs[0].InstanceID != second.ID {
			t.Fatalf("events %+v %v", evs, err)
		}
		if none, _ := s.ListEvents(ctx, sc, EventFilter{Before: at}); len(none) != 0 {
			t.Fatal("Before filter ignored")
		}

		rule, err := s.CreateMappingRule(ctx, MappingRule{Scope: sc, MatchType: "image_repo", Pattern: "x", ServiceID: svc.ID})
		if err != nil {
			t.Fatal(err)
		}
		if rules, err := s.ListMappingRules(ctx, sc); err != nil || len(rules) != 1 || rules[0].ID != rule.ID {
			t.Fatalf("rules %+v %v", rules, err)
		}
	})
}

func TestHousekeep(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		for i := range 8 {
			id := NewID()
			if _, err := s.InsertSnapshot(ctx, Snapshot{
				ID: id, Scope: f.ws.Scope(), TargetID: f.tgt.ID,
				CollectedAt: base.Add(time.Duration(i) * time.Minute), Complete: true, Payload: json.RawMessage(`{}`),
			}); err != nil {
				t.Fatal(err)
			}
			if i < 6 { // the last two stay unprocessed
				if err := s.MarkSnapshotProcessed(ctx, f.ws.Scope(), id); err != nil {
					t.Fatal(err)
				}
			}
		}
		u, _ := s.CreateUser(ctx, f.ws.OrgID, "a@example.com", "", RoleViewer)
		_ = s.CreateSession(ctx, "expired", u.ID, -time.Hour, Session{})
		_ = s.CreateSession(ctx, "valid", u.ID, time.Hour, Session{})

		r, err := s.Housekeep(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		if r.Snapshots != 4 || r.Sessions != 1 {
			t.Fatalf("removed %+v", r)
		}
		left, _ := s.UnprocessedSnapshots(ctx, 10)
		if len(left) != 2 {
			t.Fatal("unprocessed snapshots were removed")
		}
		if n, _ := s.CountSnapshots(ctx); n != 4 {
			t.Fatalf("%d snapshots left, want 2 newest processed + 2 unprocessed", n)
		}
		if _, _, err := s.SessionUser(ctx, "valid"); err != nil {
			t.Fatal("valid session removed")
		}
		if again, _ := s.Housekeep(ctx, 2); again.Snapshots != 0 {
			t.Fatal("second run removed more")
		}
	})
}

func TestAudit(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		for _, a := range []string{"agent.create", "user.role"} {
			if err := s.Audit(ctx, AuditEntry{
				OrgID: f.ws.OrgID, WorkspaceID: f.ws.ID, Actor: "ana@example.com", Action: a,
				Details: map[string]string{"name": "eu"},
			}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.ListAudit(ctx, f.ws.OrgID, 10)
		if err != nil || len(got) != 2 || got[0].Actor != "ana@example.com" || got[0].Details["name"] != "eu" {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

func TestMemberships(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		clientA, _ := s.CreateWorkspace(ctx, ws.OrgID, "Client A", "client-a")
		clientB, _ := s.CreateWorkspace(ctx, ws.OrgID, "Client B", "client-b")
		admin, _ := s.CreateUser(ctx, ws.OrgID, "admin@msp.example", "", RoleAdmin)
		client, _ := s.CreateUser(ctx, ws.OrgID, "ops@client-a.example", "", RoleViewer)

		all, err := s.UserWorkspaces(ctx, admin)
		if err != nil || len(all) != 3 || all[1].Role != RoleAdmin {
			t.Fatalf("admin access %+v %v", all, err)
		}
		if none, _ := s.UserWorkspaces(ctx, client); len(none) != 0 {
			t.Fatalf("client sees %+v before being invited", none)
		}
		if err := s.SetMembership(ctx, client.ID, clientA.ID, RoleViewer); err != nil {
			t.Fatal(err)
		}
		if err := s.SetMembership(ctx, client.ID, clientA.ID, RoleMember); err != nil { // change role
			t.Fatal(err)
		}
		got, _ := s.UserWorkspaces(ctx, client)
		if len(got) != 1 || got[0].Workspace.ID != clientA.ID || got[0].Role != RoleMember {
			t.Fatalf("client access %+v", got)
		}
		if roles, _ := s.WorkspaceRoles(ctx, clientB.ID); len(roles) != 0 {
			t.Fatal("client B has members")
		}
		if w, err := s.GetWorkspaceBySlug(ctx, ws.OrgID, "CLIENT-A"); err != nil || w.ID != clientA.ID {
			t.Fatalf("by slug %+v %v", w, err)
		}
		if c, _ := s.CountWorkspace(ctx, clientA.ID); c.Members != 1 {
			t.Fatalf("counts %+v", c)
		}
		_ = s.RemoveMembership(ctx, client.ID, clientA.ID)
		if got, _ := s.UserWorkspaces(ctx, client); len(got) != 0 {
			t.Fatal("membership not removed")
		}
	})
}

// Migration 9 rebuilds the SQLite targets table. Rows that reference targets must
// survive it, and foreign keys must be enforced again afterwards.
func TestTargetsRebuildKeepsData(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "sqlite://"+t.TempDir()+"/goliash.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	f := setup(t, s)
	if ok, err := s.InsertSnapshot(ctx, Snapshot{
		ID: NewID(), Scope: f.ws.Scope(), TargetID: f.tgt.ID, AgentID: f.agnt.ID,
		CollectedAt: time.Now(), Complete: true, Payload: json.RawMessage(`{}`),
	}); err != nil || !ok {
		t.Fatal(err)
	}

	provider, err := s.migrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}

	var snapshots, fkOn int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM snapshots WHERE target_id = ?`, f.tgt.ID).Scan(&snapshots); err != nil || snapshots != 1 {
		t.Fatalf("snapshots after rebuild: %d %v", snapshots, err)
	}
	if got, err := s.GetTarget(ctx, f.ws.Scope(), f.tgt.ID); err != nil || got.Name != f.tgt.Name || got.AgentID != f.agnt.ID {
		t.Fatalf("target after rebuild: %+v %v", got, err)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fkOn); err != nil || fkOn != 1 {
		t.Fatalf("foreign_keys = %d %v", fkOn, err)
	}
	rows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign key violations after rebuild")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO snapshots (id, org_id, workspace_id, target_id, collected_at, received_at, complete, payload)
		VALUES ('x', 'o', ?, 'no-such-target', ?, ?, TRUE, '{}')`, f.ws.ID, time.Now(), time.Now()); err == nil {
		t.Fatal("snapshot for a missing target accepted: foreign keys are off")
	}
}

func TestPasswordsAndSessions(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		u, _ := s.CreateUser(ctx, ws.OrgID, "a@example.com", "", RoleViewer)
		if u.HasPassword {
			t.Fatal("new user has a password")
		}
		for _, id := range []string{"one", "two", "three"} {
			if err := s.CreateSession(ctx, id, u.ID, time.Hour, Session{Method: "link", UserAgent: "ua-" + id, IP: "10.0.0.1"}); err != nil {
				t.Fatal(err)
			}
		}
		list, err := s.ListSessions(ctx, u.ID)
		if err != nil || len(list) != 3 || list[0].UserAgent == "" || list[0].Method != "link" || list[0].LastSeen.IsZero() {
			t.Fatalf("sessions %+v %v", list, err)
		}

		// Setting a password keeps only the session that set it.
		if err := s.SetUserPassword(ctx, u.ID, "$argon2id$x", "one"); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetUser(ctx, u.ID)
		if !got.HasPassword || got.PasswordChangedAt.IsZero() {
			t.Fatalf("user %+v", got)
		}
		if h, _ := s.UserPasswordHash(ctx, u.ID); h != "$argon2id$x" {
			t.Fatalf("hash %q", h)
		}
		if list, _ = s.ListSessions(ctx, u.ID); len(list) != 1 || list[0].ID != "one" {
			t.Fatalf("sessions after password change %+v", list)
		}
		if su, method, err := s.SessionUser(ctx, "one"); err != nil || !su.HasPassword || method != "link" {
			t.Fatalf("session user %+v %v", su, err)
		}

		_ = s.CreateSession(ctx, "four", u.ID, time.Hour, Session{})
		if err := s.DeleteUserSession(ctx, "someone-else", "four"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted another user's session: %v", err)
		}
		if n, err := s.DeleteUserSessions(ctx, u.ID, "one"); err != nil || n != 1 {
			t.Fatalf("sign out others: %d %v", n, err)
		}

		// Removing the password signs out everywhere.
		if err := s.SetUserPassword(ctx, u.ID, "", ""); err != nil {
			t.Fatal(err)
		}
		if got, _ = s.GetUser(ctx, u.ID); got.HasPassword || !got.PasswordChangedAt.IsZero() {
			t.Fatalf("password not removed: %+v", got)
		}
		if list, _ = s.ListSessions(ctx, u.ID); len(list) != 0 {
			t.Fatalf("sessions left %+v", list)
		}
		if err := s.SetUserName(ctx, u.ID, "Ana"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAPITokens(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		other, _ := s.CreateWorkspace(ctx, ws.OrgID, "Other", "other")
		ro, err := s.CreateAPIToken(ctx, ws.Scope(), APIToken{Name: "grafana", CreatedBy: "ana@example.com"}, "h1")
		if err != nil || ro.Role != RoleViewer {
			t.Fatalf("default role: %+v %v", ro, err)
		}
		_, _ = s.CreateAPIToken(ctx, ws.Scope(), APIToken{Name: "old", Role: RoleMember, ExpiresAt: time.Now().Add(-time.Hour)}, "h2")
		_, _ = s.CreateAPIToken(ctx, other.Scope(), APIToken{Name: "theirs"}, "h3")

		sc, tok, err := s.APITokenAuth(ctx, "h1")
		if err != nil || sc.WorkspaceID != ws.ID || tok.Name != "grafana" || tok.Role != RoleViewer {
			t.Fatalf("auth %+v %+v %v", sc, tok, err)
		}
		if _, _, err := s.APITokenAuth(ctx, "h2"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expired token works: %v", err)
		}
		list, _ := s.ListAPITokens(ctx, ws.Scope())
		if len(list) != 2 {
			t.Fatalf("list %+v", list)
		}
		byName := map[string]APIToken{}
		for _, x := range list {
			byName[x.Name] = x
		}
		if g := byName["grafana"]; g.LastUsed.IsZero() || g.CreatedBy != "ana@example.com" || g.Expired(time.Now()) {
			t.Fatalf("grafana %+v", g)
		}
		if !byName["old"].Expired(time.Now()) {
			t.Fatal("old not expired")
		}
		if err := s.RevokeAPIToken(ctx, other.Scope(), ro.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("revoked from another workspace: %v", err)
		}
		if err := s.RevokeAPIToken(ctx, ws.Scope(), ro.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.APITokenAuth(ctx, "h1"); !errors.Is(err, ErrNotFound) {
			t.Fatal("revoked token works")
		}
		if list, _ = s.ListAPITokens(ctx, ws.Scope()); len(list) != 1 {
			t.Fatalf("revoked token listed: %+v", list)
		}
	})
}

func TestAgentAdministration(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		sc := ws.Scope()
		a, err := s.CreateAgent(ctx, sc, "eu", "old")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := s.CreateAgent(ctx, sc, "us", "us-token")
		env, _ := s.CreateEnvironment(ctx, sc, "prod", 30)
		tgt, _ := s.CreateTarget(ctx, Target{Scope: sc, EnvironmentID: env.ID, AgentID: a.ID, Platform: "kubernetes", Name: "k8s"})

		// Rotation: both tokens work until the new one is used.
		if _, err := s.AgentByTokenHash(ctx, "old"); err != nil {
			t.Fatal(err)
		}
		if err := s.AddAgentToken(ctx, sc, a.ID, "new1"); err != nil {
			t.Fatal(err)
		}
		// Rotating again before the agent switched replaces the unused new token.
		if err := s.AddAgentToken(ctx, sc, a.ID, "new2"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetAgent(ctx, sc, a.ID); got.ActiveTokens != 2 {
			t.Fatalf("active tokens during rotation: %d", got.ActiveTokens)
		}
		if _, err := s.AgentByTokenHash(ctx, "new1"); !errors.Is(err, ErrNotFound) {
			t.Fatal("replaced rotation token works")
		}
		if _, err := s.AgentByTokenHash(ctx, "old"); err != nil {
			t.Fatal("old token stopped before the new one was used")
		}
		if got, err := s.AgentByTokenHash(ctx, "new2"); err != nil || got.ActiveTokens != 1 {
			t.Fatalf("new token: %+v %v", got, err)
		}
		if _, err := s.AgentByTokenHash(ctx, "old"); !errors.Is(err, ErrNotFound) {
			t.Fatal("old token works after the new one was used")
		}
		if toks, _ := s.AgentTokens(ctx, sc, a.ID); len(toks) != 1 || toks[0].LastUsed.IsZero() {
			t.Fatalf("tokens %+v", toks)
		}
		if _, err := s.AgentByTokenHash(ctx, "us-token"); err != nil {
			t.Fatal("rotation touched another agent")
		}

		if err := s.RenameAgent(ctx, sc, a.ID, "us"); !errors.Is(err, ErrExists) {
			t.Fatalf("rename onto a taken name: %v", err)
		}
		if err := s.RenameAgent(ctx, sc, a.ID, "eu-1"); err != nil {
			t.Fatal(err)
		}

		if err := s.DeleteAgent(ctx, sc, a.ID); !errors.Is(err, ErrInUse) {
			t.Fatalf("deleted an agent with targets: %v", err)
		}
		if err := s.SetTargetAgent(ctx, sc, tgt.ID, "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("moved to an unknown agent: %v", err)
		}
		if err := s.SetTargetAgent(ctx, sc, tgt.ID, b.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetTarget(ctx, sc, tgt.ID); got.AgentID != b.ID {
			t.Fatalf("target agent %q", got.AgentID)
		}
		if err := s.DeleteAgent(ctx, sc, a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AgentByTokenHash(ctx, "new2"); !errors.Is(err, ErrNotFound) {
			t.Fatal("deleted agent's token works")
		}
		if err := s.SetTargetAgent(ctx, sc, tgt.ID, ""); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteTarget(ctx, sc, tgt.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetTarget(ctx, sc, tgt.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("target not deleted")
		}

		// Rotating an agent that never connected leaves only the new token.
		c, _ := s.CreateAgent(ctx, sc, "fresh", "lost")
		_ = s.AddAgentToken(ctx, sc, c.ID, "replacement")
		if got, _ := s.GetAgent(ctx, sc, c.ID); got.ActiveTokens != 1 {
			t.Fatalf("never-connected agent has %d tokens after rotation", got.ActiveTokens)
		}
		if _, err := s.AgentByTokenHash(ctx, "lost"); !errors.Is(err, ErrNotFound) {
			t.Fatal("lost token works")
		}

		// Revoked agents show no working token.
		_ = s.RevokeAgentTokens(ctx, sc, b.ID)
		if got, _ := s.GetAgent(ctx, sc, b.ID); got.ActiveTokens != 0 {
			t.Fatalf("revoked agent has %d tokens", got.ActiveTokens)
		}
	})
}

func TestManageConfiguration(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		sc := ws.Scope()
		dev, _ := s.CreateEnvironment(ctx, sc, "dev", 10)
		prod, _ := s.CreateEnvironment(ctx, sc, "prod", 30)
		if err := s.UpdateEnvironment(ctx, sc, dev.ID, "prod", 10); !errors.Is(err, ErrExists) {
			t.Fatalf("rename onto a taken name: %v", err)
		}
		if err := s.UpdateEnvironment(ctx, sc, dev.ID, "development", 5); err != nil {
			t.Fatal(err)
		}
		tgt, _ := s.CreateTarget(ctx, Target{Scope: sc, EnvironmentID: prod.ID, Platform: "swarm", Name: "s"})
		if err := s.DeleteEnvironment(ctx, sc, prod.ID); !errors.Is(err, ErrInUse) {
			t.Fatalf("deleted an environment with targets: %v", err)
		}
		if err := s.UpdateTarget(ctx, sc, tgt.ID, dev.ID, json.RawMessage(`{"swarm":{}}`), 120); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetTarget(ctx, sc, tgt.ID)
		var settings map[string]any
		_ = json.Unmarshal(got.Settings, &settings)
		if _, ok := settings["swarm"]; got.EnvironmentID != dev.ID || got.PollIntervalSeconds != 120 || !ok || len(settings) != 1 {
			t.Fatalf("target %+v", got)
		}
		if err := s.DeleteEnvironment(ctx, sc, prod.ID); err != nil {
			t.Fatal(err)
		}

		ch, _ := s.CreateChannel(ctx, Channel{Scope: sc, Type: "slack", Name: "ops", Config: json.RawMessage(`{}`)})
		r, _ := s.CreateRule(ctx, Rule{Scope: sc, ChannelID: ch.ID, Mode: "instant"})
		r2, _ := s.CreateRule(ctx, Rule{Scope: sc, ChannelID: ch.ID, Mode: "daily"})
		if err := s.SetRulePaused(ctx, sc, r.ID, true); err != nil {
			t.Fatal(err)
		}
		if act, _ := s.ActiveRules(ctx, sc); len(act) != 1 || act[0].ID != r2.ID {
			t.Fatalf("active rules %+v", act)
		}
		if all, _ := s.ListRules(ctx, sc); len(all) != 2 || !all[0].Paused {
			t.Fatalf("rules %+v", all)
		}
		if err := s.DeleteRule(ctx, sc, r2.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteChannel(ctx, sc, ch.ID); err != nil {
			t.Fatal(err)
		}
		if all, _ := s.ListRules(ctx, sc); len(all) != 0 {
			t.Fatal("rules of a deleted channel remain")
		}

		svc, _ := s.EnsureService(ctx, sc, "web")
		mr, _ := s.CreateMappingRule(ctx, MappingRule{Scope: sc, MatchType: "ignore", Pattern: "^x$"})
		if err := s.DeleteMappingRule(ctx, sc, mr.ID); err != nil {
			t.Fatal(err)
		}
		ack, _ := s.CreateAck(ctx, Ack{Scope: sc, ServiceID: svc.ID, Kind: "release", UntilVersion: "2"})
		other, _ := s.CreateWorkspace(ctx, ws.OrgID, "Other", "other")
		if err := s.DeleteAck(ctx, other.Scope(), ack.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("deleted an ack of another workspace")
		}
		if err := s.DeleteAck(ctx, sc, ack.ID); err != nil {
			t.Fatal(err)
		}
	})
}

func TestChannelServiceWorkspaceEdits(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		sc := ws.Scope()
		a, _ := s.CreateChannel(ctx, Channel{Scope: sc, Type: "slack", Name: "a", Config: json.RawMessage(`{"url":"https://x"}`)})
		_, _ = s.CreateChannel(ctx, Channel{Scope: sc, Type: "slack", Name: "b", Config: json.RawMessage(`{}`)})
		if err := s.UpdateChannel(ctx, sc, a.ID, "b", json.RawMessage(`{}`)); !errors.Is(err, ErrExists) {
			t.Fatalf("rename onto b: %v", err)
		}
		if err := s.UpdateChannel(ctx, sc, a.ID, "a2", json.RawMessage(`{"url":"https://y"}`)); err != nil {
			t.Fatal(err)
		}
		chans, _ := s.ListChannels(ctx, sc)
		if chans[0].Name != "a2" || !strings.Contains(string(chans[0].Config), "https://y") {
			t.Fatalf("channel %+v", chans[0])
		}

		f := setup(t, s)
		svc, _ := s.EnsureService(ctx, f.ws.Scope(), "runs")
		snap := NewID()
		_, _ = s.InsertSnapshot(ctx, Snapshot{ID: snap, Scope: f.ws.Scope(), TargetID: f.tgt.ID, CollectedAt: time.Now(), Complete: true, Payload: json.RawMessage(`{}`)})
		if err := s.ApplySnapshot(ctx, SnapshotChanges{Scope: f.ws.Scope(), SnapshotID: snap, TargetID: f.tgt.ID, At: time.Now(), Upsert: []Instance{{
			TargetID: f.tgt.ID, EnvironmentID: f.env.ID, ServiceID: svc.ID, WorkloadID: "w", WorkloadKind: "deployment", WorkloadName: "w",
			ContainerName: "c", Image: "x:1", Tag: "1", Running: 1, IsMain: true,
		}}}); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteService(ctx, f.ws.Scope(), svc.ID); !errors.Is(err, ErrInUse) {
			t.Fatalf("deleted a running service: %v", err)
		}
		idle, _ := s.EnsureService(ctx, f.ws.Scope(), "idle")
		if err := s.DeleteService(ctx, f.ws.Scope(), idle.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetService(ctx, f.ws.Scope(), idle.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("service not deleted")
		}

		if err := s.RenameWorkspace(ctx, ws.OrgID, ws.ID, "Renamed"); err != nil {
			t.Fatal(err)
		}
		if err := s.RenameWorkspace(ctx, "other-org", ws.ID, "x"); !errors.Is(err, ErrNotFound) {
			t.Fatal("renamed a workspace of another organization")
		}
	})
}

func TestQueuesAndBackup(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		_, _ = s.InsertSnapshot(ctx, Snapshot{ID: NewID(), Scope: f.ws.Scope(), TargetID: f.tgt.ID, CollectedAt: time.Now(), Payload: json.RawMessage(`{}`)})
		q, err := s.Queues(ctx, f.ws.Scope())
		if err != nil || q.PendingSnapshots != 1 || q.QueuedNotifications != 0 {
			t.Fatalf("queues %+v %v", q, err)
		}
		path := t.TempDir() + "/copy.db"
		err = s.Backup(ctx, path)
		if s.Dialect() == Postgres {
			if !errors.Is(err, ErrBackupUnsupported) {
				t.Fatalf("postgres backup: %v", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Backup(ctx, path); !errors.Is(err, os.ErrExist) {
			t.Fatalf("overwrote a backup: %v", err)
		}
		copyStore, err := Open(ctx, "sqlite://"+path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = copyStore.Close() }()
		if q, _ := copyStore.Queues(ctx, f.ws.Scope()); q.PendingSnapshots != 1 {
			t.Fatalf("backup lacks the snapshot: %+v", q)
		}
	})
}

func TestTOTPState(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		u, _ := s.CreateUser(ctx, ws.OrgID, "a@example.com", "", RoleViewer)
		if err := s.StartTOTP(ctx, u.ID, "JBSWY3DPEHPK3PXP"); err != nil {
			t.Fatal(err)
		}
		tt, err := s.UserTOTP(ctx, u.ID)
		if err != nil || tt.Secret != "JBSWY3DPEHPK3PXP" || tt.Enabled {
			t.Fatalf("pending %+v %v", tt, err)
		}
		if got, _ := s.GetUser(ctx, u.ID); got.TOTPEnabled {
			t.Fatal("enabled before confirming")
		}
		if err := s.EnableTOTP(ctx, u.ID, 100, []string{"h1", "h2"}); err != nil {
			t.Fatal(err)
		}
		if err := s.StartTOTP(ctx, u.ID, "OTHER"); !errors.Is(err, ErrExists) {
			t.Fatalf("restarted while on: %v", err)
		}
		if got, _ := s.GetUser(ctx, u.ID); !got.TOTPEnabled {
			t.Fatal("not enabled")
		}
		if ok, _ := s.UseTOTPStep(ctx, u.ID, 100); ok {
			t.Fatal("confirming step reused")
		}
		if ok, _ := s.UseTOTPStep(ctx, u.ID, 101); !ok {
			t.Fatal("next step refused")
		}
		if ok, _ := s.UseRecoveryCode(ctx, u.ID, "h1"); !ok {
			t.Fatal("recovery code refused")
		}
		if ok, _ := s.UseRecoveryCode(ctx, u.ID, "h1"); ok {
			t.Fatal("recovery code reused")
		}
		if tt, _ = s.UserTOTP(ctx, u.ID); tt.Codes != 1 {
			t.Fatalf("codes left %d", tt.Codes)
		}
		_ = s.ReplaceRecoveryCodes(ctx, u.ID, []string{"h3"})
		if ok, _ := s.UseRecoveryCode(ctx, u.ID, "h2"); ok {
			t.Fatal("replaced code works")
		}
		if err := s.DisableTOTP(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
		if tt, _ = s.UserTOTP(ctx, u.ID); tt.Enabled || tt.Secret != "" || tt.Codes != 0 {
			t.Fatalf("after disable %+v", tt)
		}

		// Sign-in tokens only work for their purpose.
		_ = s.CreateLoginToken(ctx, "mfa", u.ID, time.Minute, TokenSecondFactor, "password")
		if _, _, err := s.ConsumeLoginToken(ctx, "mfa", TokenLink, TokenRecovery); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second-factor token used as a link: %v", err)
		}
		if got, purpose, method, err := s.PeekLoginToken(ctx, "mfa", TokenSecondFactor); err != nil || got.ID != u.ID || purpose != TokenSecondFactor || method != "password" {
			t.Fatalf("peek %v %s %s %v", got.ID, purpose, method, err)
		}
		if _, purpose, err := s.ConsumeLoginToken(ctx, "mfa", TokenSecondFactor); err != nil || purpose != TokenSecondFactor {
			t.Fatal(err)
		}
		if _, _, _, err := s.PeekLoginToken(ctx, "mfa", TokenSecondFactor); !errors.Is(err, ErrNotFound) {
			t.Fatal("used token still pending")
		}
	})
}

func TestSessionIdleAndRequire2FA(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		u, _ := s.CreateUser(ctx, ws.OrgID, "a@example.com", "", RoleViewer)
		start := time.Now()
		s.now = func() time.Time { return start }
		_ = s.CreateSession(ctx, "idle", u.ID, 30*24*time.Hour, Session{Method: "password"})
		_ = s.CreateSession(ctx, "busy", u.ID, 30*24*time.Hour, Session{Method: "oidc"})
		s.SetSessionIdle(14 * 24 * time.Hour)

		s.now = func() time.Time { return start.Add(10 * 24 * time.Hour) }
		if _, m, err := s.SessionUser(ctx, "busy"); err != nil || m != "oidc" { // used: last seen moves
			t.Fatalf("busy session: %v", err)
		}
		s.now = func() time.Time { return start.Add(20 * 24 * time.Hour) }
		if _, _, err := s.SessionUser(ctx, "idle"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("idle session still works: %v", err)
		}
		if _, _, err := s.SessionUser(ctx, "busy"); err != nil {
			t.Fatalf("recently used session ended: %v", err)
		}

		if on, err := s.RequireTwoFactor(ctx, ws.OrgID); err != nil || on {
			t.Fatalf("default %v %v", on, err)
		}
		if err := s.SetRequireTwoFactor(ctx, ws.OrgID, true); err != nil {
			t.Fatal(err)
		}
		if on, _ := s.RequireTwoFactor(ctx, ws.OrgID); !on {
			t.Fatal("not required")
		}
	})
}

func TestWorkspaceAppLabel(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		if key, err := s.WorkspaceAppLabel(ctx, f.ws.ID); err != nil || key != "" {
			t.Fatalf("default %q %v", key, err)
		}
		if err := s.SetWorkspaceAppLabel(ctx, f.ws.OrgID, f.ws.ID, "example.com/app"); err != nil {
			t.Fatal(err)
		}
		if key, err := s.WorkspaceAppLabel(ctx, f.ws.ID); err != nil || key != "example.com/app" {
			t.Fatalf("stored %q %v", key, err)
		}
		if err := s.SetWorkspaceAppLabel(ctx, "other-org", f.ws.ID, "x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("another organization changed it: %v", err)
		}
	})
}
