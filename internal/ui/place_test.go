// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"testing"

	"github.com/pipozzz/goliash/internal/versions"
)

// Grouped by place, a service running in two AWS accounts is listed under each, with its versions there.
func TestGroupByPlace(t *testing.T) {
	row := MatrixRow{
		Service: "api", Health: "ok",
		Places: map[string]string{"t-dev": "AWS 625853223232 · eu-central-1", "t-prod": "AWS 098225058563 · eu-central-1"},
		Cells: []MatrixCell{
			{Versions: []VersionView{{Tag: "1.2", On: []TargetTag{{ID: "t-dev", Name: "dev-ecs"}}}}},
			{Versions: []VersionView{{Tag: "1.1", On: []TargetTag{{ID: "t-prod", Name: "prod-ecs"}}}}},
		},
	}
	other := MatrixRow{Service: "db", Health: "ok", Cells: []MatrixCell{{Versions: []VersionView{{Tag: "16", On: []TargetTag{{ID: "t-vm", Name: "vm"}}}}}, {}}}
	gs := groupRows([]MatrixRow{row, other}, "place")
	if len(gs) != 3 || gs[0].Name != "AWS 098225058563 · eu-central-1" || gs[1].Name != "AWS 625853223232 · eu-central-1" || gs[2].Name != "place unknown" {
		t.Fatalf("groups %+v", gs)
	}
	if prod := gs[0].Rows[0]; len(prod.Cells[0].Versions) != 0 || prod.Cells[1].Versions[0].Tag != "1.1" {
		t.Fatalf("prod row %+v", prod)
	}
	if versions.PlaceOf(map[string]string{"cloud": "gcp", "account": "acme", "region": "europe-west1"}) != "GCP acme · europe-west1" || versions.PlaceOf(nil) != "" {
		t.Error("PlaceOf")
	}
}
