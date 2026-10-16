// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLicensesAndImagesCommands(t *testing.T) {
	ctx := context.Background()
	dsn := t.TempDir() + "/x.db"
	if err := run(ctx, []string{"demo", "-database", dsn}, io.Discard); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(ctx, []string{"licenses", "-database", dsn}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "network-copyleft") || !strings.Contains(out.String(), "com.itextpdf/itextpdf") ||
		!strings.Contains(out.String(), "payments-api (prod)") || strings.Contains(out.String(), "permissive") {
		t.Errorf("licenses:\n%s", out.String())
	}
	out.Reset()
	if err := run(ctx, []string{"licenses", "-database", dsn, "-class", "all", "-fail-on", "network-copyleft"}, &out); !errors.Is(err, errFound) {
		t.Errorf("fail-on: %v", err)
	}
	if !strings.Contains(out.String(), "permissive") {
		t.Errorf("every class:\n%s", out.String())
	}
	if err := run(ctx, []string{"licenses", "-database", dsn, "-fail-on", "source-available"}, io.Discard); err != nil {
		t.Errorf("nothing source-available runs: %v", err)
	}

	out.Reset()
	if err := run(ctx, []string{"images", "-database", dsn, "payments-api"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ENVIRONMENT", "prod", "ghcr.io/acme/payments-api:", " MB", "yes", "Last change in prod"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("images misses %q:\n%s", want, out.String())
		}
	}
	if err := run(ctx, []string{"images", "-database", dsn, "nope"}, io.Discard); err == nil {
		t.Error("unknown service")
	}
}
