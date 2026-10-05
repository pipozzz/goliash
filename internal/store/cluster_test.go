// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestLeaderSQLiteAlwaysLeads(t *testing.T) {
	s, err := Open(context.Background(), "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	l := s.NewLeader(slog.New(slog.NewTextHandler(io.Discard, nil)))
	started := make(chan struct{})
	done := make(chan struct{})
	go func() { l.Run(ctx, func(context.Context) { close(started) }); close(done) }()
	<-started
	if !l.IsLeader() || s.Clustered() {
		t.Fatal("SQLite server does not lead")
	}
	if err := s.Notify(ctx, "x", "y"); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
}

func TestLeaderElectionAndNotify(t *testing.T) {
	dsn := os.Getenv("GOLIASH_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOLIASH_TEST_POSTGRES_DSN not set")
	}
	s := openPostgresSchema(t, dsn)
	defer func(r, h time.Duration) { leaderRetry, leaderHeartbeat = r, h }(leaderRetry, leaderHeartbeat)
	leaderRetry, leaderHeartbeat = 100*time.Millisecond, 100*time.Millisecond
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Two servers compete; exactly one runs the work.
	var running atomic.Int32
	work := func(ctx context.Context) {
		running.Add(1)
		go func() { <-ctx.Done(); running.Add(-1) }()
	}
	ctxA, stopA := context.WithCancel(context.Background())
	ctxB, stopB := context.WithCancel(context.Background())
	defer stopB()
	a, b := s.NewLeader(log), s.NewLeader(log)
	go a.Run(ctxA, work)
	waitFor(t, func() bool { return a.IsLeader() })
	go b.Run(ctxB, work)
	time.Sleep(500 * time.Millisecond)
	if b.IsLeader() || running.Load() != 1 {
		t.Fatalf("two leaders: a=%v b=%v running=%d", a.IsLeader(), b.IsLeader(), running.Load())
	}

	// The leader stops; the other takes over.
	stopA()
	waitFor(t, func() bool { return b.IsLeader() && !a.IsLeader() && running.Load() == 1 })

	// Notifications reach listeners.
	got := make(chan string, 1)
	lctx, stopL := context.WithCancel(context.Background())
	defer stopL()
	go s.Listen(lctx, []string{"goliash_test"}, log, func(_, payload string) { got <- payload })
	deadline := time.After(5 * time.Second)
	for {
		_ = s.Notify(context.Background(), "goliash_test", "ws-1")
		select {
		case p := <-got:
			if p != "ws-1" {
				t.Fatalf("payload %q", p)
			}
			return
		case <-deadline:
			t.Fatal("no notification")
		case <-time.After(200 * time.Millisecond): // LISTEN may not be set up yet
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 100 {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met in 5s")
}
