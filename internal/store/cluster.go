// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
)

// Several servers may share one PostgreSQL database. They all serve the UI, the API
// and agents; one of them, the leader, also runs the background work (processing
// snapshots, checking upstreams, sending notifications, housekeeping).
//
// Leadership is a lease: a row the leader renews every few seconds, with expiry
// times from the database's clock so the servers' clocks do not matter. When the
// leader stops renewing (it crashed, froze, or lost its network), another server
// takes the lease once it expires. A leader that cannot renew in time stops its work
// at its own deadline, before the lease can pass to someone else. With SQLite there
// is only one server, and it is always the leader.

// Lease timing.
const (
	leaseTTL   = 15 * time.Second // how long a lease lasts without renewal
	leaseRenew = 5 * time.Second  // how often the leader renews; followers try as often
)

// Leader runs work while this server leads. work starts the background loops with
// the context it gets and returns; that context ends when leadership ends.
type Leader struct {
	store  *Store
	log    *slog.Logger
	id     string
	leader atomic.Bool
	until  atomic.Int64 // the local deadline of the lease, Unix nanoseconds

	ttl, renew time.Duration
	// beforeRenew, when set, runs before each renewal (tests freeze the leader here).
	beforeRenew func()
}

// NewLeader prepares leader election.
func (s *Store) NewLeader(log *slog.Logger) *Leader {
	return &Leader{store: s, log: log, id: NewID(), ttl: leaseTTL, renew: leaseRenew}
}

// IsLeader reports whether this server runs the background work now.
func (l *Leader) IsLeader() bool {
	return l.leader.Load() && (l.store.dialect != Postgres || time.Now().UnixNano() < l.until.Load())
}

// Run competes for leadership until ctx ends, calling work each time this server
// becomes the leader.
func (l *Leader) Run(ctx context.Context, work func(ctx context.Context)) {
	if l.store.dialect != Postgres {
		l.leader.Store(true)
		work(ctx)
		<-ctx.Done()
		l.leader.Store(false)
		return
	}
	for ctx.Err() == nil {
		got, err := l.acquire(ctx)
		if err != nil && ctx.Err() == nil {
			l.log.Warn("leader election", "err", err)
		}
		if got {
			l.lead(ctx, work)
			continue // lost the lease: compete again at once
		}
		select {
		case <-ctx.Done():
		case <-time.After(l.renew):
		}
	}
}

// acquire takes the lease when it is free, expired or already ours.
func (l *Leader) acquire(ctx context.Context) (bool, error) {
	res, err := l.store.db.ExecContext(ctx, `INSERT INTO leases (name, holder, expires_at) VALUES ('leader', $1, now() + $2::interval)
		ON CONFLICT (name) DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
		WHERE leases.expires_at < now() OR leases.holder = EXCLUDED.holder`, l.id, l.ttl.String())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// lead runs work and renews the lease until ctx ends or the lease is lost.
func (l *Leader) lead(ctx context.Context, work func(ctx context.Context)) {
	l.log.Info("this server is the leader: it runs the background work")
	// The local deadline is the lease's end as seen from here, a little early, so the
	// work stops before the lease can pass to another server, even after a freeze.
	deadline := &l.until
	extend := func() { deadline.Store(time.Now().Add(l.ttl - l.renew/2).UnixNano()) }
	expired := func() bool { return time.Now().UnixNano() > deadline.Load() }
	extend()
	workCtx, stopWork := context.WithCancel(ctx)
	l.leader.Store(true)
	defer func() {
		stopWork()
		l.leader.Store(false)
		if ctx.Err() != nil { // stopping on purpose: hand over at once
			_, _ = l.store.db.ExecContext(context.WithoutCancel(ctx),
				`UPDATE leases SET expires_at = now() WHERE name = 'leader' AND holder = $1`, l.id)
		}
	}()
	go func() { // watchdog: stop the work when the deadline passes without renewal
		for {
			t := time.NewTimer(time.Until(time.Unix(0, deadline.Load())))
			select {
			case <-workCtx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			if expired() {
				stopWork()
				return
			}
		}
	}()
	work(workCtx)
	tick := time.NewTicker(l.renew)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-workCtx.Done():
			l.log.Warn("the leader lease ran out before it was renewed; stopping the background work")
			return
		case <-tick.C:
		}
		if l.beforeRenew != nil {
			l.beforeRenew()
		}
		if expired() {
			l.log.Warn("the leader lease ran out before it was renewed; stopping the background work")
			return
		}
		rctx, rcancel := context.WithTimeout(ctx, l.renew)
		res, err := l.store.db.ExecContext(rctx, `UPDATE leases SET expires_at = now() + $2::interval WHERE name = 'leader' AND holder = $1`,
			l.id, l.ttl.String())
		rcancel()
		if err != nil {
			l.log.Warn("renewing the leader lease failed", "err", err)
			continue // try again until the deadline
		}
		if n, _ := res.RowsAffected(); n != 1 {
			l.log.Warn("another server holds the leader lease now; stopping the background work")
			return
		}
		extend()
	}
}

// Notify sends payload to every server listening on channel (PostgreSQL); with
// SQLite there are no other servers and it does nothing.
func (s *Store) Notify(ctx context.Context, channel, payload string) error {
	if s.dialect != Postgres {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `SELECT pg_notify($1, $2)`, channel, payload)
	return err
}

// Clustered reports whether several servers may share this database.
func (s *Store) Clustered() bool { return s.dialect == Postgres }

// Listen calls fn for every notification on channels until ctx ends, reconnecting
// after errors. It does nothing with SQLite.
func (s *Store) Listen(ctx context.Context, channels []string, log *slog.Logger, fn func(channel, payload string)) {
	if s.dialect != Postgres {
		return
	}
	for ctx.Err() == nil {
		err := s.listen(ctx, channels, fn)
		if ctx.Err() != nil {
			return
		}
		log.Warn("listening for changes from other servers failed; retrying", "err", err)
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

func (s *Store) listen(ctx context.Context, channels []string, fn func(channel, payload string)) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return conn.Raw(func(dc any) error {
		sc, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("unexpected driver connection %T", dc)
		}
		pc := sc.Conn()
		for _, ch := range channels {
			if _, err := pc.Exec(ctx, "LISTEN "+quoteIdent(ch)); err != nil {
				return err
			}
		}
		for {
			n, err := pc.WaitForNotification(ctx)
			if err != nil {
				return err
			}
			fn(n.Channel, n.Payload)
		}
	})
}

func quoteIdent(s string) string {
	out := []byte{'"'}
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			out = append(out, '"')
		}
		out = append(out, s[i])
	}
	return string(append(out, '"'))
}
