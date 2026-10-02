// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package tokens

import (
	"strings"
	"testing"
)

func TestNewTokenIsValid(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		tok, hash := New(Agent)
		if !strings.HasPrefix(tok, "glsh_agent_") || len(tok) != len("glsh_agent_")+36 {
			t.Fatalf("unexpected token shape %q", tok)
		}
		if !Valid(tok, Agent) {
			t.Fatalf("new token %q is not valid", tok)
		}
		if hash != Hash(tok) || len(hash) != 64 {
			t.Fatalf("hash mismatch for %q", tok)
		}
		if seen[tok] {
			t.Fatal("duplicate token")
		}
		seen[tok] = true
	}
}

func TestValidRejects(t *testing.T) {
	tok, _ := New(Agent)
	flipped := []byte(tok)
	if flipped[15] == 'a' {
		flipped[15] = 'b'
	} else {
		flipped[15] = 'a'
	}
	for name, s := range map[string]string{
		"wrong kind":     strings.Replace(tok, "glsh_agent_", "glsh_ci_", 1),
		"typo":           string(flipped),
		"truncated":      tok[:len(tok)-1],
		"no prefix":      strings.TrimPrefix(tok, "glsh_"),
		"empty":          "",
		"other provider": "ghp_" + tok[11:],
	} {
		if Valid(s, Agent) {
			t.Errorf("%s: %q accepted", name, s)
		}
	}
}
