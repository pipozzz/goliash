// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/cipher"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
	"github.com/oklog/ulid/v2"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Dialect is the SQL database behind a Store.
type Dialect string

// Supported dialects.
const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

//go:embed migrations
var migrations embed.FS

// Store is the data access layer. Queries are written once with "?" placeholders
// and rebound for PostgreSQL.
type Store struct {
	db      *sql.DB
	dialect Dialect
	now     func() time.Time
	aead    cipher.AEAD // encrypts secrets at rest; nil without a secret key

	sessionIdle time.Duration // sessions unused this long stop working; 0: never
}

// Open connects to the database named by dsn and applies pending migrations.
//
// dsn is either a postgres:// or postgresql:// URL, or a SQLite file path,
// optionally prefixed with sqlite:// ("sqlite://:memory:" for an in-memory database).
func Open(ctx context.Context, dsn string) (*Store, error) {
	dialect, driver, source, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driver, source)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dialect, err)
	}
	if dialect == SQLite {
		// SQLite allows one writer at a time; a single connection avoids SQLITE_BUSY
		// and keeps an in-memory database alive for the lifetime of the Store.
		db.SetMaxOpenConns(1)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to %s: %w", dialect, err)
	}

	s := &Store{db: db, dialect: dialect, now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func parseDSN(dsn string) (dialect Dialect, driver, source string, err error) {
	switch {
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		return Postgres, "pgx", dsn, nil
	case dsn == "":
		return "", "", "", errors.New("empty database DSN")
	}

	path := strings.TrimPrefix(dsn, "sqlite://")
	pragmas := url.Values{}
	for _, p := range []string{"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)", "synchronous(NORMAL)"} {
		pragmas.Add("_pragma", p)
	}
	pragmas.Set("_txlock", "immediate")
	if path == ":memory:" {
		return SQLite, "sqlite", "file::memory:?" + pragmas.Encode(), nil
	}
	return SQLite, "sqlite", "file:" + path + "?" + pragmas.Encode(), nil
}

func (s *Store) migrations() (*goose.Provider, error) {
	dir, err := fs.Sub(migrations, "migrations/"+string(s.dialect))
	if err != nil {
		return nil, err
	}
	gooseDialect := goose.DialectSQLite3
	if s.dialect == Postgres {
		gooseDialect = goose.DialectPostgres
	}
	var opts []goose.ProviderOption
	if s.dialect == Postgres {
		// Several servers may start together on one database: one migrates, the others wait.
		locker, err := lock.NewPostgresSessionLocker()
		if err != nil {
			return nil, err
		}
		opts = append(opts, goose.WithSessionLocker(locker))
	}
	provider, err := goose.NewProvider(gooseDialect, s.db, dir, opts...)
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	return provider, nil
}

func (s *Store) migrate(ctx context.Context) error {
	provider, err := s.migrations()
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate %s: %w", s.dialect, err)
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Dialect reports which database the Store uses.
func (s *Store) Dialect() Dialect { return s.dialect }

// Ping checks the database connection.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// queryer is satisfied by *sql.DB and *sql.Tx.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// rebind turns "?" placeholders into "$1", "$2", … for PostgreSQL.
// Queries in this package never contain a literal "?".
func (s *Store) rebind(query string) string {
	if s.dialect != Postgres {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Store) exec(ctx context.Context, q queryer, query string, args ...any) (sql.Result, error) {
	return q.ExecContext(ctx, s.rebind(query), args...)
}

func (s *Store) queryRow(ctx context.Context, q queryer, query string, args ...any) *sql.Row {
	return q.QueryRowContext(ctx, s.rebind(query), args...)
}

func (s *Store) query(ctx context.Context, q queryer, query string, args ...any) (*sql.Rows, error) {
	return q.QueryContext(ctx, s.rebind(query), args...)
}

// inTx runs fn in a transaction and commits it when fn returns nil.
func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// NewID returns a new ULID string.
func NewID() string { return ulid.Make().String() }

func timeOrZero(t sql.NullTime) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
