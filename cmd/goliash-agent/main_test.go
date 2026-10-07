// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

func TestAgentTokenShape(t *testing.T) {
	good := "glsh_agent_" + strings.Repeat("a", 36)
	for in, want := range map[string]string{
		good:                      "",
		"  " + good + "\n":        "",
		`"` + good + `"`:          "",
		"'" + good + "'":          "",
		"glsh_api_" + "x":         "API or CI token",
		"agent_token":             "does not start with glsh_agent_",
		good[:30]:                 "characters long",
		good + "x":                "characters long",
		"glsh_agent_ab cd" + good: "spaces or quotes",
	} {
		_, err := checkToken(cleanToken(in), "GOLIASH_AGENT_TOKEN")
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%q: %v", in, err)
		}
		if err != nil && strings.Contains(err.Error(), strings.Repeat("a", 10)) {
			t.Errorf("error prints the token: %v", err)
		}
	}
}

func TestCredential(t *testing.T) {
	code := "glsh_enroll_" + strings.Repeat("b", 36)
	t.Setenv("GOLIASH_ENROLL_CODE", "")
	t.Setenv("GOLIASH_AGENT_TOKEN", " "+code+"\n")
	if c, tok, err := credential(); err != nil || c != code || tok != "" {
		t.Fatalf("code in the token variable: %q %q %v", c, tok, err)
	}
	t.Setenv("GOLIASH_ENROLL_CODE", "glsh_agent_x")
	if _, _, err := credential(); err == nil {
		t.Fatal("a token in GOLIASH_ENROLL_CODE was accepted")
	}
}
