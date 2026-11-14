// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import "testing"

// Two pull requests on the same target are told apart by their application.
func TestPRPlace(t *testing.T) {
	a := PromotePR{WorkloadID: "demo-staging/postgres", Target: "demo-staging", App: "identity-staging"}
	b := PromotePR{WorkloadID: "demo-staging/shop-db", Target: "demo-staging", App: "webshop-staging"}
	c := PromotePR{WorkloadID: "demo-prod/postgres", Target: "demo-prod", App: "identity-prod"}
	prs := []PromotePR{a, b, c}
	for _, tc := range []struct {
		pr   PromotePR
		want string
	}{{a, "identity-staging"}, {b, "webshop-staging"}, {c, "demo-prod"}} {
		if got := prPlace(prs, tc.pr); got != tc.want {
			t.Errorf("prPlace(%s) = %q, want %q", tc.pr.WorkloadID, got, tc.want)
		}
	}
}

func TestExploitedTail(t *testing.T) {
	if got := exploitedTail(1, nil); got != " runs" {
		t.Errorf("one: %q", got)
	}
	if got := exploitedTail(2, []string{"CVE-1", "CVE-2"}); got != " run, 2 past CISA's due date: CVE-1, CVE-2" {
		t.Errorf("two: %q", got)
	}
}

func TestLeadingService(t *testing.T) {
	e := EventView{Service: "redis", ServiceURL: "/services/redis", Text: "redis @ prod: runs 7.2.4"}
	if rest, ok := leadingService(e, true); !ok || rest != " @ prod: runs 7.2.4" {
		t.Errorf("got %q %v", rest, ok)
	}
	e.Text = "redis-cache @ prod: runs 1"
	if _, ok := leadingService(e, true); ok {
		t.Error("a longer name is not the service")
	}
	if _, ok := leadingService(EventView{Service: "redis", ServiceURL: "/s", Text: "redis"}, false); ok {
		t.Error("the service page does not link the service")
	}
}
