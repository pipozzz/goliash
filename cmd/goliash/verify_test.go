// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerify(t *testing.T) {
	var got map[string]any
	passed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/check" || r.Header.Get("Authorization") != "Bearer glsh_api_x" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"image": got["image"], "digest": "sha256:aa", "passed": passed, "packages": 2, "sbom_source": "request", "signed": false,
			"findings": []map[string]any{{"rule": "exploited", "blocking": true, "detail": "exploited in the wild (CISA KEV): CVE-2021-44228"}},
		})
	}))
	defer srv.Close()
	sbom := filepath.Join(t.TempDir(), "sbom.json")
	_ = os.WriteFile(sbom, []byte(`{"bomFormat":"CycloneDX","components":[]}`), 0o600)
	var out bytes.Buffer
	err := verifyCmd(context.Background(), []string{"-server", srv.URL, "-token", "glsh_api_x", "-sbom", sbom, "-fail-on", "exploited, critical", "ghcr.io/acme/app:1.2.3"}, &out)
	if !errors.Is(err, errVerifyFailed) {
		t.Fatalf("a blocked image passed: %v", err)
	}
	if !strings.Contains(out.String(), "ghcr.io/acme/app:1.2.3 (sha256:aa): blocked") || !strings.Contains(out.String(), "BLOCK exploited: exploited in the wild") {
		t.Errorf("output:\n%s", out.String())
	}
	if rules, _ := json.Marshal(got["fail_on"]); string(rules) != `["exploited","critical"]` || got["sbom"] == nil {
		t.Errorf("request %v", got)
	}
	passed = true
	out.Reset()
	if err := verifyCmd(context.Background(), []string{"-server", srv.URL, "-token", "glsh_api_x", "-json", "ghcr.io/acme/app:1.2.3"}, &out); err != nil || !strings.Contains(out.String(), `"passed":true`) {
		t.Errorf("passing: %v %s", err, out.String())
	}
	if err := verifyCmd(context.Background(), []string{"-server", srv.URL, "-token", "wrong", "x:1"}, &out); err == nil || errors.Is(err, errVerifyFailed) {
		t.Errorf("a refused token: %v", err)
	}
}
