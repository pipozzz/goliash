// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import "testing"

func TestPasskeysNeedADomain(t *testing.T) {
	for url, want := range map[string]bool{
		"https://goliash.example.com": true,
		"http://localhost:8080":       true,
		"http://goliash.example.com":  false,
		"https://10.0.0.5":            false,
		"http://127.0.0.1:8080":       false,
	} {
		a, err := New(nil, nil, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if a.PasskeysEnabled() != want {
			t.Errorf("%s: %v", url, a.PasskeysEnabled())
		}
	}
}
