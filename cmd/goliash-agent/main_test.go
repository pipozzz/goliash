// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/pkg/agentproto"
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

func TestPlatformsAdded(t *testing.T) {
	rds := agentproto.DeclaredTarget{Platform: agentproto.Rds, Name: "rds-eu-central-1"}
	docker := agentproto.DeclaredTarget{Platform: agentproto.Docker, Name: "gw"}
	var p platforms
	p.remember([]agentproto.DeclaredTarget{docker})
	if got := p.added([]agentproto.DeclaredTarget{docker, rds}); len(got) != 1 || got[0] != "rds rds-eu-central-1" {
		t.Fatalf("added: %v", got)
	}
	if got := p.added(nil); len(got) != 0 { // gone is no reason
		t.Fatalf("gone: %v", got)
	}
	for v, want := range map[string]time.Duration{"": 15 * time.Minute, "off": 0, "5m": 5 * time.Minute} {
		t.Setenv("GOLIASH_REDISCOVER", v)
		if d, err := rediscoverEvery(); err != nil || d != want {
			t.Fatalf("%q: %v %v", v, d, err)
		}
	}
	t.Setenv("GOLIASH_REDISCOVER", "5s")
	if _, err := rediscoverEvery(); err == nil {
		t.Fatal("5s accepted")
	}
}
