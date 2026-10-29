// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// A managed AWS engine is no registry: the matrix says the agent asks AWS, and offers no credentials.
func TestUnreadManagedEngines(t *testing.T) {
	o := versions.Overview{
		Refs: map[string]versions.Reference{
			"db":  {Repo: "rds.amazonaws.com/aurora-postgresql", Tag: "16.4"},
			"api": {Repo: "registry.example.com/acme/api", Tag: "1.0.0"},
		},
		Services: map[string]store.Service{"db": {ID: "db"}, "api": {ID: "api"}},
	}
	got := unreadRegistries(o)
	if len(got) != 2 || got[0].Host != "rds.amazonaws.com" || !got[0].Managed || got[1].Managed {
		t.Fatalf("unread %+v", got)
	}
}

// Dozens of preview applications merged into one family must not widen the matrix.
func TestFamilyCaptionShort(t *testing.T) {
	members := []string{"web-a", "web-b", "web-c", "web-d", "web-e", "web-f", "web-g"}
	got := familyCaption(map[string]versions.AppFamily{"web-a": {Name: "web", Members: members}}, "web", nil)
	if got != "web-a · web-b · web-c · web-d and 3 more" {
		t.Fatalf("caption %q", got)
	}
	if got := familyCaption(map[string]versions.AppFamily{"x": {Name: "web", Members: members[:5]}}, "web", nil); strings.Count(got, "·") != 4 {
		t.Fatalf("five members are all named: %q", got)
	}
}

// The banner says what the agent got, so a missing permission shows its AWS error.
func TestUnreadShowsAgentError(t *testing.T) {
	o := versions.Overview{
		Refs:      map[string]versions.Reference{"api": {Repo: "123456789012.dkr.ecr.eu-central-1.amazonaws.com/api", Tag: "1.0"}},
		Services:  map[string]store.Service{"api": {ID: "api"}},
		CheckErrs: map[string]string{"api": "agent: AccessDeniedException: not authorized to perform: ecr:ListImages"},
	}
	got := unreadRegistries(o)
	if len(got) != 1 || !got[0].ECR || !strings.HasPrefix(shortError(got[0].Error), "AccessDeniedException") {
		t.Fatalf("unread %+v", got)
	}
}

// Tags an agent read that hold no version (commit hashes, say) are no unreadable registry.
func TestUnreadSkipsReadTags(t *testing.T) {
	o := versions.Overview{
		Refs:     map[string]versions.Reference{"api": {Repo: "123456789012.dkr.ecr.eu-central-1.amazonaws.com/api", Tag: "3f9c2a1"}},
		Services: map[string]store.Service{"api": {ID: "api"}},
		TagCount: map[string]int{"api": 40},
	}
	if got := unreadRegistries(o); len(got) != 0 {
		t.Fatalf("unread %+v", got)
	}
}

// ECR in another account refuses for want of a repository policy: Goliash says so and gives the command.
func TestECRCrossAccount(t *testing.T) {
	msg := "agent: ecr ListImages aws-logs: operation error ECR: ListImages, https response error StatusCode: 400, RequestID: x, api error AccessDeniedException: User: arn:aws:sts::672088933272:assumed-role/resource-metrics/i-0da52e1dd63167af2 is not authorized to perform: ecr:ListImages on resource: arn:aws:ecr:us-west-2:240220093055:repository/aws-logs because no resource-based policy allows the ecr:ListImages action"
	role, account, cmd, ok := ecrCrossAccount(msg, "240220093055.dkr.ecr.us-west-2.amazonaws.com/aws-logs")
	if !ok || role != "arn:aws:iam::672088933272:role/resource-metrics" || account != "240220093055" ||
		!strings.Contains(cmd, "--region us-west-2 --repository-name aws-logs") || !strings.Contains(cmd, `"AWS":"arn:aws:iam::672088933272:role/resource-metrics"`) {
		t.Fatalf("%q %q %q %v", role, account, cmd, ok)
	}
	if _, _, _, ok := ecrCrossAccount("AccessDeniedException: not authorized to perform: ecr:ListImages", "240220093055.dkr.ecr.us-west-2.amazonaws.com/aws-logs"); ok {
		t.Error("a same-account refusal taken for cross-account")
	}
}
