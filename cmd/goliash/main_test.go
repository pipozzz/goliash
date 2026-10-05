// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// Flags before the command must not silently start a server.
func TestFlagsBeforeCommand(t *testing.T) {
	err := run(context.Background(), []string{"-database", t.TempDir() + "/x.db", "matrix"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "goliash matrix -database") {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLIASH_SECRET_KEY", "")
	t.Setenv("GOLIASH_SECRET_KEY_FILE", "")

	first, src, err := secretKey(dir + "/goliash.db")
	if err != nil || len(first) != 32 || src != dir+"/goliash.key" {
		t.Fatalf("generated: %d bytes from %q, %v", len(first), src, err)
	}
	if st, _ := os.Stat(src); st.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v", st.Mode())
	}
	again, _, _ := secretKey("sqlite://" + dir + "/goliash.db")
	if string(again) != string(first) {
		t.Fatal("key not reused")
	}
	if k, _, _ := secretKey("postgres://db/goliash"); k != nil {
		t.Fatal("postgres got a generated key")
	}

	t.Setenv("GOLIASH_SECRET_KEY", strings.Repeat("ab", 32))
	if k, src, err := secretKey("postgres://db/goliash"); err != nil || len(k) != 32 || src != "GOLIASH_SECRET_KEY" {
		t.Fatalf("hex key: %v", err)
	}
	t.Setenv("GOLIASH_SECRET_KEY", "too-short")
	if _, _, err := secretKey(dir + "/goliash.db"); err == nil {
		t.Fatal("short key accepted")
	}
}

// Channels created with the CLI are encrypted, and a server without the key refuses to start.
func TestSecretKeyEndToEnd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLIASH_SECRET_KEY", "")
	t.Setenv("GOLIASH_SECRET_KEY_FILE", "")
	dsn := dir + "/goliash.db"
	ctx := context.Background()
	if err := run(ctx, []string{"channel", "create", "-database", dsn, "-type", "slack", "-name", "ops", "-url", "https://hooks.slack.com/services/T/B/x"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dsn)
	wal, _ := os.ReadFile(dsn + "-wal")
	if strings.Contains(string(b)+string(wal), "hooks.slack.com") || !strings.Contains(string(b)+string(wal), `"sealed"`) {
		t.Fatal("webhook URL in the clear in the database file")
	}
	if err := os.Rename(dir+"/goliash.key", dir+"/moved.key"); err != nil {
		t.Fatal(err)
	}
	err := run(ctx, []string{"matrix", "-database", dsn}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "does not open") {
		t.Fatalf("started with a new key: %v", err)
	}
}

func TestUserPassword(t *testing.T) {
	ctx := context.Background()
	dsn := t.TempDir() + "/x.db"
	if err := run(ctx, []string{"user", "password"}, io.Discard); err == nil || err.Error() != "-email is required" {
		t.Fatalf("dispatch: %v", err)
	}
	if err := run(ctx, []string{"login-link", "-database", dsn, "-email", "ana@example.com"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	stdin := func(s string) *os.File {
		f, err := os.CreateTemp(t.TempDir(), "stdin")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(s)
		_, _ = f.Seek(0, 0)
		return f
	}
	args := []string{"-database", dsn, "-email", "ana@example.com"}
	if err := userPassword(ctx, args, stdin("short\n"), io.Discard); err == nil {
		t.Fatal("weak password accepted")
	}
	if err := userPassword(ctx, args, stdin("correct horse staple\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	db, ws, _ := openDefault(ctx, dsn)
	u, _ := db.GetUserByEmail(ctx, ws.OrgID, "ana@example.com")
	hash, _ := db.UserPasswordHash(ctx, u.ID)
	_ = db.Close()
	if !auth.VerifyPassword("correct horse staple", hash) {
		t.Fatal("password not stored")
	}
	if err := userPassword(ctx, append(args, "-remove"), stdin(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := userPassword(ctx, []string{"-database", dsn, "-email", "nobody@example.com"}, stdin("x"), io.Discard); err == nil {
		t.Fatal("unknown user accepted")
	}
}

func TestTokenCommands(t *testing.T) {
	ctx := context.Background()
	dsn := t.TempDir() + "/x.db"
	var out strings.Builder
	if err := run(ctx, []string{"token", "create", "-database", dsn, "-name", "prom", "-role", "admin"}, &out); err == nil {
		t.Fatal("admin token created")
	}
	if err := run(ctx, []string{"token", "create", "-database", dsn, "-name", "prom", "-expires", "90d"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "API token prom (viewer, expires ") || !strings.Contains(out.String(), "glsh_api_") {
		t.Fatalf("create: %s", out.String())
	}
	out.Reset()
	if err := run(ctx, []string{"token", "list", "-database", dsn}, &out); err != nil || !strings.Contains(out.String(), "prom") || !strings.Contains(out.String(), "viewer") {
		t.Fatalf("list: %v %s", err, out.String())
	}
	if err := run(ctx, []string{"token", "revoke", "-database", dsn, "-name", "nope"}, io.Discard); err == nil {
		t.Fatal("unknown token revoked")
	}
	if err := run(ctx, []string{"token", "revoke", "-database", dsn, "-name", "prom"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	_ = run(ctx, []string{"token", "list", "-database", dsn}, &out)
	if strings.Contains(out.String(), "prom") {
		t.Fatalf("revoked token listed: %s", out.String())
	}
}

func TestParseLifetime(t *testing.T) {
	for in, want := range map[string]time.Duration{"90d": 90 * 24 * time.Hour, "36h": 36 * time.Hour, "1d12h": 36 * time.Hour} {
		if got, err := parseLifetime(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "d", "xd", "-5h", "99999d", "soon"} {
		if _, err := parseLifetime(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestAgentCommands(t *testing.T) {
	ctx := context.Background()
	dsn := t.TempDir() + "/x.db"
	var out strings.Builder
	if err := run(ctx, []string{"agent", "create", "-database", dsn, "-name", "eu"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"agent", "rotate", "-database", dsn, "-name", "nope"}, io.Discard); err == nil {
		t.Fatal("rotated an unknown agent")
	}
	out.Reset()
	if err := run(ctx, []string{"agent", "rotate", "-database", dsn, "-name", "eu"}, &out); err != nil || !strings.Contains(out.String(), "glsh_agent_") {
		t.Fatalf("rotate: %v %s", err, out.String())
	}
	if err := run(ctx, []string{"agent", "revoke", "-database", dsn, "-name", "eu"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run(ctx, []string{"agent", "list", "-database", dsn}, &out); err != nil || !strings.Contains(out.String(), "revoked") {
		t.Fatalf("list: %v %s", err, out.String())
	}
}

func TestRecoveryLink(t *testing.T) {
	ctx := context.Background()
	db, ws, err := openDefault(ctx, t.TempDir()+"/x.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, nil))

	// Nobody yet: the person becomes the owner and gets a link.
	if err := recoveryLink(ctx, db, ws, "ana@example.com", "https://goliash.example.com", log); err != nil {
		t.Fatal(err)
	}
	u, err := db.GetUserByEmail(ctx, ws.OrgID, "ana@example.com")
	if err != nil || u.Role != store.RoleOwner || !strings.Contains(logs.String(), "https://goliash.example.com/auth/magic?token=") {
		t.Fatalf("first person: %+v %v %s", u, err, logs.String())
	}

	// Someone else cannot be added through the variable.
	logs.Reset()
	if err := recoveryLink(ctx, db, ws, "mallory@example.com", "https://goliash.example.com", log); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetUserByEmail(ctx, ws.OrgID, "mallory@example.com"); !errors.Is(err, store.ErrNotFound) || strings.Contains(logs.String(), "magic?token=") {
		t.Fatalf("stranger got in: %v %s", err, logs.String())
	}

	// An existing person gets a fresh link on every start, even after signing in.
	logs.Reset()
	_ = db.CreateSession(ctx, "h", u.ID, time.Hour, store.Session{})
	if err := recoveryLink(ctx, db, ws, "ANA@example.com", "https://goliash.example.com", log); err != nil || !strings.Contains(logs.String(), "magic?token=") {
		t.Fatalf("existing person: %v %s", err, logs.String())
	}
}

func TestBackupCommandAndPruning(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dsn := dir + "/goliash.db"
	if err := run(ctx, []string{"env", "create", "-database", dsn, "-name", "prod"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	_ = os.WriteFile(dir+"/goliash.key", []byte(key), 0o600)
	t.Setenv("GOLIASH_SECRET_KEY", "")
	t.Setenv("GOLIASH_SECRET_KEY_FILE", "")
	var out strings.Builder
	if err := run(ctx, []string{"backup", "-database", dsn, "-out", dir + "/backups"}, &out); err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(strings.TrimPrefix(out.String(), "backup written to "))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no backup at %q", path)
	}
	if b, err := os.ReadFile(strings.TrimSuffix(path, ".db") + ".key"); err != nil || string(b) != key {
		t.Fatalf("key not copied: %v", err)
	}
	// The backup is a working database.
	var envs strings.Builder
	if err := run(ctx, []string{"matrix", "-database", path}, &envs); err != nil {
		t.Fatal(err)
	}

	db, _, _ := openDefault(ctx, dsn)
	defer func() { _ = db.Close() }()
	for i := range 4 {
		if _, err := backupTo(ctx, db, dsn, dir+"/daily", time.Date(2026, 10, 1+i, 3, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneBackups(dir+"/daily", 2); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(dir + "/daily/goliash-*")
	if len(left) != 4 || !strings.Contains(strings.Join(left, " "), "20261004") || strings.Contains(strings.Join(left, " "), "20261001") {
		t.Fatalf("kept %v (want the newest two .db with their .key)", left)
	}
}

func TestLogFormat(t *testing.T) {
	for _, f := range []string{"", "text", "json"} {
		if _, err := newLogger(f, slog.LevelInfo); err != nil {
			t.Errorf("%q: %v", f, err)
		}
	}
	if _, err := newLogger("xml", slog.LevelInfo); err == nil {
		t.Error("xml accepted")
	}
}

func TestUserTwoFactorReset(t *testing.T) {
	ctx := context.Background()
	dsn := t.TempDir() + "/x.db"
	_ = run(ctx, []string{"login-link", "-database", dsn, "-email", "ana@example.com"}, io.Discard)
	db, ws, _ := openDefault(ctx, dsn)
	u, _ := db.GetUserByEmail(ctx, ws.OrgID, "ana@example.com")
	_ = db.StartTOTP(ctx, u.ID, auth.NewTOTPSecret())
	_ = db.EnableTOTP(ctx, u.ID, 0, nil)
	_ = db.Close()
	if err := run(ctx, []string{"user", "2fa", "-database", dsn, "-email", "ana@example.com"}, io.Discard); err == nil {
		t.Fatal("reset without -reset")
	}
	if err := run(ctx, []string{"user", "2fa", "-database", dsn, "-email", "ana@example.com", "-reset"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	db, _, _ = openDefault(ctx, dsn)
	defer func() { _ = db.Close() }()
	if got, _ := db.GetUser(ctx, u.ID); got.TOTPEnabled {
		t.Fatal("2FA still on")
	}
}
