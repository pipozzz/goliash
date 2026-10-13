// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// TestScale times what every page load does against a large workspace. It runs only with GOLIASH_SCALE=1
// (GOLIASH_SCALE_SERVICES sets the size, default 1000) and fails when something takes far too long.
func TestScale(t *testing.T) {
	if os.Getenv("GOLIASH_SCALE") != "1" {
		t.Skip("set GOLIASH_SCALE=1")
	}
	n := 1000
	if v, err := strconv.Atoi(os.Getenv("GOLIASH_SCALE_SERVICES")); err == nil && v > 0 {
		n = v
	}
	ctx := context.Background()
	dsn := "sqlite://" + t.TempDir() + "/scale.db"
	if pg := os.Getenv("GOLIASH_TEST_POSTGRES_DSN"); pg != "" && os.Getenv("GOLIASH_SCALE_PG") == "1" {
		dsn = scaleSchema(t, pg)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	sc := ws.Scope()

	start := time.Now()
	envs := []string{"dev", "test", "staging", "prod"}
	var targets []store.Target
	for i, name := range envs {
		e, err := st.CreateEnvironment(ctx, sc, name, (i+1)*10)
		if err != nil {
			t.Fatal(err)
		}
		for k := range 5 {
			tg, err := st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: e.ID, Platform: "kubernetes", Name: fmt.Sprintf("%s-%d", name, k)})
			if err != nil {
				t.Fatal(err)
			}
			targets = append(targets, tg)
		}
	}
	svcIDs := make([]string, n)
	for i := range n {
		svc, err := st.EnsureService(ctx, sc, fmt.Sprintf("svc-%04d", i))
		if err != nil {
			t.Fatal(err)
		}
		svcIDs[i] = svc.ID
		var rel []string
		for m := range 30 {
			rel = append(rel, fmt.Sprintf("1.%d.0", m))
		}
		if _, err := st.InsertReleases(ctx, sc, svc.ID, rel); err != nil {
			t.Fatal(err)
		}
	}
	// Three rounds of deployments: every service runs on one target per environment, later ones newer.
	base := time.Now().Add(-72 * time.Hour)
	for round := range 3 {
		for ti, tg := range targets {
			ch := store.SnapshotChanges{Scope: sc, TargetID: tg.ID, SnapshotID: store.NewID(), At: base.Add(time.Duration(round*24) * time.Hour)}
			if _, err := st.InsertSnapshot(ctx, store.Snapshot{ID: ch.SnapshotID, Scope: sc, TargetID: tg.ID, CollectedAt: ch.At, Complete: true, Payload: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			existing, _ := st.ListTargetInstances(ctx, sc, tg.ID)
			for _, i := range existing {
				if i.Active() {
					ch.Remove = append(ch.Remove, i.ID)
				}
			}
			for i := ti % 5; i < n; i += 5 {
				v := fmt.Sprintf("1.%d.0", 20+round+(ti/5))
				ch.Upsert = append(ch.Upsert, store.Instance{
					ID: store.NewID(), TargetID: tg.ID, EnvironmentID: tg.EnvironmentID, ServiceID: svcIDs[i], WorkloadID: fmt.Sprintf("w%d", i),
					WorkloadKind: "deployment", WorkloadName: fmt.Sprintf("svc-%04d", i), ContainerName: "app",
					Image: fmt.Sprintf("ghcr.io/acme/svc-%04d:%s", i, v), Tag: v, Digest: fmt.Sprintf("sha256:%064d", i*10+round), Running: 2, IsMain: true,
				})
			}
			if err := st.ApplySnapshot(ctx, ch); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("seeded %d services, %d targets, 3 rounds in %s", n, len(targets), time.Since(start).Round(time.Millisecond))

	checker := NewChecker(st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	if err := checker.EvaluateDrift(ctx, sc); err != nil {
		t.Logf("drift: %v", err)
	}
	limit := time.Duration(n) * 2 * time.Millisecond // 2 s for 1000 services: generous, a page should be far faster
	timeIt := func(name string, f func() error) {
		t.Helper()
		begin := time.Now()
		if err := f(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cold := time.Since(begin)
		begin = time.Now()
		_ = f() // again, as the next page load finds the caches
		d := time.Since(begin)
		t.Logf("%-28s %8s cold, %8s warm", name, cold.Round(100*time.Microsecond), d.Round(100*time.Microsecond))
		if d > limit {
			t.Errorf("%s took %s, more than %s", name, d, limit)
		}
	}
	var o Overview
	timeIt("LoadOverview (matrix)", func() (err error) { o, err = LoadOverview(ctx, st, sc); return })
	timeIt("Updates", func() error { _ = Updates(o, nil, time.Now()); return nil })
	timeIt("LoadVulnReport", func() error { _, err := LoadVulnReport(ctx, st, sc, ""); return err })
	timeIt("OverviewAt (time travel)", func() error { _, err := OverviewAt(ctx, st, sc, base.Add(30*time.Hour)); return err })
	timeIt("Inventory", func() error { _, err := Inventory(ctx, st, sc, nil); return err })
	timeIt("LoadHealth (status)", func() error { _, err := LoadHealth(ctx, st, nil, sc, "1.0.0", false); return err })
}

// scaleSchema is dsn with a schema of its own, dropped after the test, like the store's PostgreSQL tests.
func scaleSchema(t *testing.T, dsn string) string {
	t.Helper()
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "scale_" + strings.ToLower(store.NewID())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); _ = admin.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
