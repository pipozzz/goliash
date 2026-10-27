// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package storetest gives tests a migrated SQLite database quickly: the migrations run once per test binary,
// and each test gets a copy. Migrating for every test took most of some packages' time under the race detector.
package storetest

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

var migrated struct {
	once sync.Once
	db   []byte
	err  error
}

// DSN returns a fresh copy of a migrated SQLite database for store.Open, removed when the test ends.
func DSN(t testing.TB) string {
	t.Helper()
	migrated.once.Do(func() {
		dir, err := os.MkdirTemp("", "goliash-storetest")
		if err != nil {
			migrated.err = err
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		path := filepath.Join(dir, "goliash.db")
		st, err := store.Open(context.Background(), path)
		if err != nil {
			migrated.err = err
			return
		}
		if migrated.err = st.Close(); migrated.err == nil {
			migrated.db, migrated.err = os.ReadFile(path) //nolint:gosec // a file this function just made
		}
	})
	if migrated.err != nil {
		t.Fatal(migrated.err)
	}
	path := filepath.Join(t.TempDir(), "goliash.db")
	if err := os.WriteFile(path, migrated.db, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
