// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"strings"
	"testing"
)

// Grouped by target, a service shows under each cluster or account it runs on, with only that target's versions;
// targets come in environment order, and a service running nowhere comes last.
func TestGroupRowsByTarget(t *testing.T) {
	v := func(tag string, targets ...string) VersionView {
		vv := VersionView{Tag: tag}
		for _, n := range targets {
			vv.On = append(vv.On, TargetTag{ID: "id-" + n, Name: n, Caption: "Kubernetes · " + n[:strings.Index(n, "-")]})
		}
		return vv
	}
	rows := []MatrixRow{
		{Service: "api", Cells: []MatrixCell{{Versions: []VersionView{v("2.0", "dev-k8s")}}, {Versions: []VersionView{v("1.9", "prod-eu", "prod-us"), v("1.8", "prod-us")}}}},
		{Service: "web", Cells: []MatrixCell{{}, {Versions: []VersionView{v("3.1", "prod-eu")}}}},
		{Service: "ghost", Cells: []MatrixCell{{}, {}}},
	}
	groups := groupRows(rows, "target")
	var got []string
	for _, g := range groups {
		var svcs []string
		for _, r := range g.Rows {
			var tags []string
			for _, c := range r.Cells {
				for _, x := range c.Versions {
					tags = append(tags, x.Tag)
				}
			}
			svcs = append(svcs, r.Service+"="+strings.Join(tags, "+"))
		}
		got = append(got, g.Name+"("+g.Caption+"):"+strings.Join(svcs, ","))
	}
	want := "dev-k8s(Kubernetes · dev):api=2.0 | prod-eu(Kubernetes · prod):api=1.9,web=3.1 | prod-us(Kubernetes · prod):api=1.9+1.8 | not running():ghost="
	if strings.Join(got, " | ") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, " | "), want)
	}
	if groups[0].URL != "/targets/id-dev-k8s" || groups[3].URL != "" {
		t.Fatalf("target pages %q %q", groups[0].URL, groups[3].URL)
	}
}
