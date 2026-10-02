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
			Running: 3, IsMain: true,
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
		if err != nil || len(active) != 1 || active[0].Tag != "1.5.0" || active[0].Running != 2 || !active[0].IsMain {
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
		_ = s.CreateSession(ctx, "expired", u.ID, -time.Hour)
		_ = s.CreateSession(ctx, "valid", u.ID, time.Hour)

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
		if _, err := s.SessionUser(ctx, "valid"); err != nil {
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
