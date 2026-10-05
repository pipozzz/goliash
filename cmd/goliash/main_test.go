// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/auth"
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
