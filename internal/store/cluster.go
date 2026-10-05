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
// snapshots, checking upstreams, sending notifications, housekeeping). Leadership is
// a PostgreSQL advisory lock held on its own connection: when the leader stops or
// loses its connection, the lock goes and another server takes over. With SQLite
// there is only one server, and it is always the leader.

// leaderLock is the advisory lock key: "goliash" in ASCII, as a bigint.
const leaderLock int64 = 0x676f6c69617368

// Leadership timing.
const (
	leaderRetry     = 15 * time.Second // how often a follower tries to become leader
	leaderHeartbeat = 10 * time.Second // how often the leader checks its lock connection
)

// Leader runs work while this server leads. work starts the background loops with
// the context it gets and returns; that context ends when leadership ends.
type Leader struct {
	store  *Store
	log    *slog.Logger
	leader atomic.Bool

	retry, heartbeat time.Duration
}

// NewLeader prepares leader election.
func (s *Store) NewLeader(log *slog.Logger) *Leader {
	return &Leader{store: s, log: log, retry: leaderRetry, heartbeat: leaderHeartbeat}
}

// IsLeader reports whether this server runs the background work now.
func (l *Leader) IsLeader() bool { return l.leader.Load() }

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
		held, err := l.lead(ctx, work)
		if err != nil && ctx.Err() == nil {
			l.log.Warn("leader election", "err", err)
		}
		if held {
			continue // lost leadership: try again at once
		}
		select {
		case <-ctx.Done():
		case <-time.After(l.retry):
		}
	}
}

// lead takes the lock if it is free and holds it while running work. held reports
// whether this server was the leader.
func (l *Leader) lead(ctx context.Context, work func(ctx context.Context)) (held bool, err error) {
	conn, err := l.store.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, leaderLock).Scan(&got); err != nil || !got {
		return false, err
	}
	l.log.Info("this server is the leader: it runs the background work")
	l.leader.Store(true)
	wctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		l.leader.Store(false)
		// Release explicitly; closing the connection would too.
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, leaderLock)
	}()
	work(wctx)
	tick := time.NewTicker(l.heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, nil
		case <-tick.C:
			hctx, hcancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.PingContext(hctx)
			hcancel()
			if err != nil {
				l.log.Warn("lost the leader lock connection; stopping the background work", "err", err)
				return true, err
			}
		}
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
