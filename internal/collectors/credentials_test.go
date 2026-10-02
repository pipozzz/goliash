// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package collectors

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredential(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLIASH_CREDENTIALS_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "nomad-prod"), []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, err := Credential("nomad-prod"); err != nil || got != "file-secret" {
		t.Fatalf("from file: %q %v", got, err)
	}
	t.Setenv("GOLIASH_CREDENTIAL_NOMAD_PROD", " env-secret ")
	if got, err := Credential("nomad-prod"); err != nil || got != "env-secret" {
		t.Fatalf("env wins: %q %v", got, err)
	}
	if _, err := Credential("missing"); !errors.Is(err, ErrNoCredential) || !strings.Contains(err.Error(), "GOLIASH_CREDENTIAL_MISSING") {
		t.Fatalf("missing: %v", err)
	}
	for _, bad := range []string{"", "../etc/passwd", "a/b", ".."} {
		if _, err := Credential(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSplitImage(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	for in, want := range map[string][2]string{
		"nginx:1.27.2@" + d:    {"nginx:1.27.2", d},
		"nginx:1.27.2":         {"nginx:1.27.2", ""},
		"nginx@sha256:short":   {"nginx@sha256:short", ""},
		"ghcr.io/a/b:1.0@" + d: {"ghcr.io/a/b:1.0", d},
	} {
		img, dig := SplitImage(in)
		if img != want[0] || dig != want[1] {
			t.Errorf("SplitImage(%q) = %q, %q", in, img, dig)
		}
	}
}
