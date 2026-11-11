// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"maps"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A page asks for the same whole tables several times (the running instances for the overview, the vulnerability
// report, hygiene, licenses …), and every list allocates thousands of rows the garbage collector then sweeps: on a
// small host that, not the queries, is what makes pages slow. The hottest of them are kept until a write to one of
// their tables: every write through exec counts the tables it changes (in a transaction, once more at commit), and
// a kept list of an older count is read again. Another process's writes (PostgreSQL with several replicas) do not
// count, so a kept list also expires after cacheTTL.

const cacheTTL = 30 * time.Second

var writtenTable = regexp.MustCompile(`(?is)^\s*(insert(?:\s+or\s+\w+)?\s+into|replace\s+into|update|delete\s+from)\s+([a-z_]+)`)

// anyDelete is counted for every delete too: one can cascade to other tables (a target's instances), so every kept
// list depends on it.
const anyDelete = "*delete"

// tableGens counts writes per table.
type tableGens struct {
	mu   sync.Mutex
	gens map[string]*atomic.Uint64
	txs  map[*sql.Tx]map[string]bool // tables a transaction wrote, counted again when it commits
}

func (t *tableGens) counter(table string) *atomic.Uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.gens == nil {
		t.gens = map[string]*atomic.Uint64{}
	}
	g := t.gens[table]
	if g == nil {
		g = &atomic.Uint64{}
		t.gens[table] = g
	}
	return g
}

// wrote counts a write to the table a statement changes (and, for a delete, to anyDelete).
func (t *tableGens) wrote(q queryer, query string) {
	m := writtenTable.FindStringSubmatch(query)
	if m == nil {
		return
	}
	tables := []string{strings.ToLower(m[2])}
	if strings.HasPrefix(strings.ToLower(m[1]), "delete") {
		tables = append(tables, anyDelete)
	}
	for _, table := range tables {
		t.counter(table).Add(1)
	}
	if tx, ok := q.(*sql.Tx); ok {
		t.mu.Lock()
		if t.txs == nil {
			t.txs = map[*sql.Tx]map[string]bool{}
		}
		if t.txs[tx] == nil {
			t.txs[tx] = map[string]bool{}
		}
		for _, table := range tables {
			t.txs[tx][table] = true
		}
		t.mu.Unlock()
	}
}

// done counts a transaction's tables once more after it ended, so nothing read while it was open is kept.
func (t *tableGens) done(tx *sql.Tx) {
	t.mu.Lock()
	tables := t.txs[tx]
	delete(t.txs, tx)
	t.mu.Unlock()
	for table := range tables {
		t.counter(table).Add(1)
	}
}

// key is the tables' counts, which a kept value must have been read at.
func (t *tableGens) key(tables []string) string {
	var b strings.Builder
	for _, table := range append([]string{anyDelete}, tables...) {
		b.WriteString(table)
		b.WriteByte('=')
		b.WriteString(uitoa(t.counter(table).Load()))
		b.WriteByte(';')
	}
	return b.String()
}

func uitoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

type cached struct {
	gens string
	at   time.Time
	v    any
}

// readCache keeps lists read from tables until one of them is written.
type readCache struct {
	mu sync.Mutex
	m  map[string]cached
}

// keep returns the value read under name while the tables are unchanged, else reads it with load and keeps it when
// nothing was written to them meanwhile.
func keep[T any](s *Store, name string, tables []string, load func() (T, error)) (T, error) {
	before := s.tables.key(tables)
	s.cache.mu.Lock()
	if c, ok := s.cache.m[name]; ok && c.gens == before && time.Since(c.at) < cacheTTL {
		s.cache.mu.Unlock()
		return c.v.(T), nil
	}
	s.cache.mu.Unlock()
	v, err := load()
	if err != nil {
		return v, err
	}
	if s.tables.key(tables) == before {
		s.cache.mu.Lock()
		if s.cache.m == nil {
			s.cache.m = map[string]cached{}
		}
		s.cache.m[name] = cached{gens: before, at: time.Now(), v: v}
		s.cache.mu.Unlock()
	}
	return v, nil
}

// Generations is the write counts of tables, for a cache of something read from them: it is stale once they differ.
func (s *Store) Generations(tables ...string) string { return s.tables.key(tables) }

// copySlice returns a slice the caller may change without changing what is kept.
func copySlice[T any](v []T) []T {
	if v == nil {
		return nil
	}
	return append(make([]T, 0, len(v)), v...)
}

// copyMap returns a map the caller may change without changing what is kept.
func copyMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return nil
	}
	return maps.Clone(m)
}

// ListActiveInstances returns the instances present in each target's latest snapshot.
func (s *Store) ListActiveInstances(ctx context.Context, sc Scope) ([]Instance, error) {
	v, err := keep(s, "instances "+sc.WorkspaceID, []string{"instances"}, func() ([]Instance, error) { return s.listActiveInstances(ctx, sc) })
	return copySlice(v), err
}

// ListServices returns the workspace's services by name.
func (s *Store) ListServices(ctx context.Context, sc Scope) ([]Service, error) {
	v, err := keep(s, "services "+sc.WorkspaceID, []string{"services"}, func() ([]Service, error) { return s.listServices(ctx, sc) })
	return copySlice(v), err
}

// ListEnvironments returns a workspace's environments in promotion order.
func (s *Store) ListEnvironments(ctx context.Context, sc Scope) ([]Environment, error) {
	v, err := keep(s, "environments "+sc.WorkspaceID, []string{"environments"}, func() ([]Environment, error) { return s.listEnvironments(ctx, sc) })
	return copySlice(v), err
}

// ListImageSBOMs returns the workspace's SBOMs keyed by repo + "@" + digest.
func (s *Store) ListImageSBOMs(ctx context.Context, sc Scope) (map[string]ImageSBOM, error) {
	v, err := keep(s, "image_sboms "+sc.WorkspaceID, []string{"image_sboms"}, func() (map[string]ImageSBOM, error) { return s.listImageSBOMs(ctx, sc) })
	return copyMap(v), err
}

// ListVulns returns every vulnerability record kept, by ID.
func (s *Store) ListVulns(ctx context.Context) (map[string]Vuln, error) {
	v, err := keep(s, "osv_vulns", []string{"osv_vulns"}, func() (map[string]Vuln, error) { return s.listVulns(ctx) })
	return copyMap(v), err
}

type kevList struct {
	m  map[string]Exploited
	at time.Time
}

// KnownExploited returns the catalog by CVE, and when it was last fetched (zero when never).
func (s *Store) KnownExploited(ctx context.Context) (map[string]Exploited, time.Time, error) {
	v, err := keep(s, "known_exploited", []string{"known_exploited"}, func() (kevList, error) {
		m, at, err := s.knownExploited(ctx)
		return kevList{m, at}, err
	})
	return copyMap(v.m), v.at, err
}
