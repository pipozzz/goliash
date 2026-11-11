// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package pkgscan

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// The packages a Go executable links are read from its line table, stripped or not: net/http for a program that
// serves HTTP, not net/smtp it never imports.
func TestGoLinked(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import (
	"fmt"
	"net/http"
)

func main() {
	fmt.Println(http.CanonicalHeaderKey("x-a"))
	_ = http.ListenAndServe(":8080", nil)
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/prog\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "prog")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-ldflags=-s -w", "-o", bin, ".") //nolint:gosec // the test builds its own program into a temp dir
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("no go to build with: %v %s", err, out)
	}
	b, err := os.ReadFile(bin) //nolint:gosec // the test's own build
	if err != nil {
		t.Fatal(err)
	}
	var s Scanner
	s.Add("app/prog", b)
	got := s.GoLinked()
	for _, p := range []string{"net/http", "fmt", "crypto/tls"} {
		if !slices.Contains(got, p) {
			t.Errorf("%s not linked: %v", p, got)
		}
	}
	for _, p := range []string{"net/smtp", "archive/zip", "encoding/xml"} {
		if slices.Contains(got, p) {
			t.Errorf("%s linked, though never imported", p)
		}
	}
	// A Go executable whose line table is unreadable leaves nothing to say.
	var u Scanner
	u.Add("app/prog", b)
	u.unread = true
	if u.GoLinked() != nil {
		t.Error("an unreadable executable still claims what is not linked")
	}
}

func TestPackageOf(t *testing.T) {
	for in, want := range map[string]string{
		"net/http.(*Server).Serve":                   "net/http",
		"net/http.CanonicalHeaderKey":                "net/http",
		"golang.org/x/net/http2.(*Framer).ReadFrame": "golang.org/x/net/http2",
		"slices.Sort[...]":                           "slices",
		"main.main.func1":                            "main",
		"type:.eq.[2]string":                         "",
	} {
		if got := PackageOf(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}
