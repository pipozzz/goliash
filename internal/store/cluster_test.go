// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fast := func() *Leader {
		l := s.NewLeader(log)
		l.ttl, l.renew = 600*time.Millisecond, 100*time.Millisecond
		return l
	}

	// Two servers compete; exactly one runs the work.
	var running atomic.Int32
	work := func(ctx context.Context) {
		running.Add(1)
		go func() { <-ctx.Done(); running.Add(-1) }()
	}
	ctxA, stopA := context.WithCancel(context.Background())
	ctxB, stopB := context.WithCancel(context.Background())
	a, b := fast(), fast()
	doneA, doneB := make(chan struct{}), make(chan struct{})
	defer func() { stopA(); stopB(); <-doneA; <-doneB }() // no goroutine outlives the test
	go func() { a.Run(ctxA, work); close(doneA) }()
	waitFor(t, func() bool { return a.IsLeader() })
	go func() { b.Run(ctxB, work); close(doneB) }()
	time.Sleep(500 * time.Millisecond)
	if b.IsLeader() || running.Load() != 1 {
		t.Fatalf("two leaders: a=%v b=%v running=%d", a.IsLeader(), b.IsLeader(), running.Load())
	}

	// The leader stops; the other takes over.
	stopA()
	waitFor(t, func() bool { return b.IsLeader() && !a.IsLeader() && running.Load() == 1 })

	// A frozen leader (it stops renewing, as after a VM pause or a lost network) loses
	// the lease: another server takes over, and the frozen one stops its work.
	ctxC, stopC := context.WithCancel(context.Background())
	freeze := make(chan struct{})
	var frozen atomic.Bool
	c := fast()
	c.beforeRenew = func() {
		if frozen.Load() {
			<-freeze
		}
	}
	doneC := make(chan struct{})
	defer func() { stopC(); <-doneC }()
	// Thaw c whatever happens, or a failing check leaves it frozen and the deferred
	// wait above hangs the test until its timeout.
	var thaw sync.Once
	defer thaw.Do(func() { close(freeze) })
	go func() { c.Run(ctxC, work); close(doneC) }()
	stopB() // hand over from b to c
	waitFor(t, func() bool { return c.IsLeader() })
	frozen.Store(true)
	d := fast()
	ctxD, stopD := context.WithCancel(context.Background())
	doneD := make(chan struct{})
	defer func() { stopD(); <-doneD }()
	go func() { d.Run(ctxD, work); close(doneD) }()
	waitFor(t, func() bool { return d.IsLeader() })
	if c.IsLeader() {
		t.Fatal("frozen leader still leads")
	}
	// c's watchdog stops its work on its own goroutine, a moment after its deadline.
	waitFor(t, func() bool { return running.Load() == 1 })
	thaw.Do(func() { close(freeze) })
	time.Sleep(300 * time.Millisecond)
	if c.IsLeader() || !d.IsLeader() {
		t.Fatal("the thawed leader took the lease back")
	}

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
