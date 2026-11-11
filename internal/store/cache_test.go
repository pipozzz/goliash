// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// What is kept is read again after any write to its tables: a plain write, a transaction, a cascading delete.
func TestReadCache(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, migratedDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	sc := ws.Scope()
	if envs, _ := st.ListEnvironments(ctx, sc); len(envs) != 0 {
		t.Fatalf("environments before any: %v", envs)
	}
	if _, err := st.CreateEnvironment(ctx, sc, "prod", 1); err != nil {
		t.Fatal(err)
	}
	envs, _ := st.ListEnvironments(ctx, sc) // read again after the write
	if len(envs) != 1 {
		t.Fatalf("a new environment not seen: %v", envs)
	}
	tg, _ := st.CreateTarget(ctx, Target{Scope: sc, EnvironmentID: envs[0].ID, Platform: "kubernetes", Name: "c1"})

	count := func() int {
		t.Helper()
		ins, err := st.ListActiveInstances(ctx, sc)
		if err != nil {
			t.Fatal(err)
		}
		return len(ins)
	}
	if count() != 0 {
		t.Fatal("instances before any")
	}
	// A transaction (ApplySnapshot) writes them: read again.
	in := Instance{TargetID: tg.ID, EnvironmentID: envs[0].ID, WorkloadID: "w", WorkloadKind: "deployment", WorkloadName: "w", ContainerName: "c", Image: "nginx:1.27", Tag: "1.27", Running: 1, IsMain: true}
	if err := st.ApplySnapshot(ctx, SnapshotChanges{Scope: sc, TargetID: tg.ID, SnapshotID: NewID(), At: time.Now(), Upsert: []Instance{in}}); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatal("a snapshot's instances not seen")
	}
	// What is returned is the caller's own: changing it does not change what is kept.
	ins, _ := st.ListActiveInstances(ctx, sc)
	ins[0].Image = "changed"
	if again, _ := st.ListActiveInstances(ctx, sc); again[0].Image != "nginx:1.27" {
		t.Fatal("a caller changed the kept list")
	}
	// Deleting the target cascades to its instances: read again.
	if err := st.DeleteTarget(ctx, sc, tg.ID); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Fatal("a deleted target's instances still listed")
	}
	// A plain write.
	if _, err := st.EnsureService(ctx, sc, "api"); err != nil {
		t.Fatal(err)
	}
	if svcs, _ := st.ListServices(ctx, sc); len(svcs) != 1 {
		t.Fatalf("services %d", len(svcs))
	}
	if err := st.SetKnownExploited(ctx, []Exploited{{CVE: "CVE-1", DateAdded: "2026-01-01"}}); err != nil {
		t.Fatal(err)
	}
	if kev, _, _ := st.KnownExploited(ctx); len(kev) != 1 {
		t.Fatal("catalog not read again")
	}
}

func TestWrittenTable(t *testing.T) {
	for q, want := range map[string]string{
		"INSERT INTO instances (id) VALUES (?)":        "instances",
		"\n\t\tINSERT OR IGNORE INTO osv_vulns (id)":   "osv_vulns",
		"UPDATE services SET name = ?":                 "services",
		"delete from known_exploited":                  "known_exploited",
		"SELECT * FROM instances":                      "",
		"WITH x AS (SELECT 1) SELECT * FROM instances": "",
	} {
		got := ""
		if m := writtenTable.FindStringSubmatch(q); m != nil {
			got = m[2]
		}
		if got != want {
			t.Errorf("%q: %q, want %q", q, got, want)
		}
	}
}

// SQLite: a page reads while a long write is under way, instead of waiting for it.
func TestReadersBesideWriter(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, migratedDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if st.Dialect() != SQLite {
		t.Skip("SQLite only")
	}
	if st.rdb == nil {
		t.Fatal("no reader connections")
	}
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	writing, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- st.inTx(ctx, func(tx *sql.Tx) error {
			if _, err := st.exec(ctx, tx, `UPDATE workspaces SET name = name WHERE id = ?`, ws.ID); err != nil {
				return err
			}
			close(writing)
			<-release // a long write
			return nil
		})
	}()
	<-writing
	start := time.Now()
	if _, err := st.listServices(ctx, ws.Scope()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Error("a read waited for the write")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The readers only read.
	if _, err := st.rdb.ExecContext(ctx, `UPDATE workspaces SET name = name`); err == nil {
		t.Error("a reader connection wrote")
	}
}
