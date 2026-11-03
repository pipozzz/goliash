// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// A Docker host is where its agent is; an ECS cluster read through AWS's API shares only its agent's cloud and
// account, and has its own region (and an ARN's account).
func TestTargetWhere(t *testing.T) {
	agent := map[string]string{"cloud": "aws", "account": "098225058563", "region": "eu-central-1", "instance": "i-0f13", "os": "Amazon Linux 2023"}
	docker := TargetWhere(store.Target{Platform: "docker", Settings: json.RawMessage(`{}`)}, agent)
	if docker["instance"] != "i-0f13" || docker["region"] != "eu-central-1" || docker["platform"] != "docker" {
		t.Errorf("docker: %v", docker)
	}
	ecs := TargetWhere(store.Target{Platform: "ecs", Settings: json.RawMessage(`{"ecs":{"region":"eu-west-1","clusters":["arn:aws:ecs:eu-west-1:625853223232:cluster/dev"]}}`)}, agent)
	if ecs["region"] != "eu-west-1" || ecs["account"] != "625853223232" || ecs["instance"] != "" || ecs["os"] != "" || ecs["cloud"] != "aws" {
		t.Errorf("ecs: %v", ecs)
	}
	if got := strings.Join(WhereTokens(map[string]string{"os": "Amazon Linux 2023", "region": "eu-west-1"}), " "); got != "os:amazon linux 2023 region:eu-west-1" {
		t.Errorf("tokens %q", got)
	}
	if server := TargetWhere(store.Target{Platform: "kubernetes"}, nil); len(server) != 1 {
		t.Errorf("server's own target: %v", server)
	}
}

func TestSplitWhere(t *testing.T) {
	where, rest := SplitWhere("openssl Region:EU-Central account:6258 foo:bar")
	if strings.Join(where, " ") != "region:eu-central account:6258" || rest != "openssl foo:bar" {
		t.Fatalf("%v %q", where, rest)
	}
	tokens := []string{"account:625853223232", "os:amazon linux 2023", "region:eu-central-1"}
	if !WhereMatches(tokens, where) || WhereMatches(tokens, []string{"region:eu-west"}) || !WhereMatches(tokens, []string{"os:amazon"}) {
		t.Error("matching")
	}
}
