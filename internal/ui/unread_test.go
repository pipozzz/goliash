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
