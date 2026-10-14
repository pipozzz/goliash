// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSummary(t *testing.T) {
	e := newPublicEnv(t)
	code, body := e.call(http.DefaultClient, http.MethodGet, "/api/v1/summary", e.token, "")
	var s map[string]any
	if code != http.StatusOK || json.Unmarshal([]byte(body), &s) != nil {
		t.Fatalf("%d %s", code, body)
	}
	// A token's request names its workspace too: the widget's heading.
	if s["workspace"] == "" || s["production"] == "" || s["generated"] == "" {
		t.Errorf("summary misses names: %s", body)
	}
	for _, k := range []string{"services", "drifting", "updates", "exploited", "behind", "median_exposure_days", "behind_90_days", "end_of_life_passed", "stale_targets"} {
		if _, ok := s[k].(float64); !ok {
			t.Errorf("summary misses %s: %s", k, body)
		}
	}
	if code, _ := e.call(http.DefaultClient, http.MethodGet, "/api/v1/summary", "glsh_api_wrong", ""); code != http.StatusUnauthorized {
		t.Errorf("a wrong token: %d", code)
	}
}
