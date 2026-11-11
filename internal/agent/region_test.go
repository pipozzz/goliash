// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"testing"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

// An agent that found its region on EC2 has no AWS_REGION: engine versions are asked in its AWS targets' region.
func TestAWSRegionFromTargets(t *testing.T) {
	a := &Agent{runners: map[string]*runningTarget{}}
	if a.awsRegion() != "" {
		t.Fatal("a region without AWS targets")
	}
	spec := func(t agentproto.Target) []byte { b, _ := json.Marshal(t); return b }
	a.runners["a"] = &runningTarget{spec: spec(agentproto.Target{Platform: agentproto.Docker, Docker: &agentproto.DockerSettings{DockerHost: "tcp://p:2375"}})}
	a.runners["b"] = &runningTarget{spec: spec(agentproto.Target{Platform: agentproto.Rds, Rds: &agentproto.RDSSettings{Region: "eu-central-1"}})}
	if got := a.awsRegion(); got != "eu-central-1" {
		t.Fatalf("region %q", got)
	}
}
