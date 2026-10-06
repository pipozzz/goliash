// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"strings"
	"testing"
)

func TestEnvState(t *testing.T) {
	v := []VersionView{{Tag: "1.0.0"}}
	for _, c := range []struct {
		cell   MatrixCell
		latest string
		want   string
	}{
		{MatrixCell{}, "1.0.0", stNone},
		{MatrixCell{Versions: v}, "1.0.0", stCurrent},
		{MatrixCell{Versions: v}, "", stUnknown},
		{MatrixCell{Versions: v, Drifts: []DriftBadge{{Kind: "upstream"}}}, "1.1.0", stBehind},
		{MatrixCell{Versions: v, Drifts: []DriftBadge{{Kind: "upstream"}, {Kind: "eol"}}}, "2.0.0", stAttention},
	} {
		if got := envState(c.cell, c.latest); got != c.want {
			t.Errorf("%+v: %s, want %s", c.cell, got, c.want)
		}
	}
	if worse(stCurrent, stAttention) != stAttention || worse(stBehind, stNone) != stBehind {
		t.Error("worse")
	}
}

// The favicon has nine cells; a single service in need of attention still shows warm,
// in the logo's warm place (middle right).
func TestFaviconSVG(t *testing.T) {
	svg := faviconSVG(TileCounts{Current: 40, Attention: 1})
	if strings.Count(svg, "<rect") != 10 || strings.Count(svg, "#ffb347") != 1 || !strings.Contains(svg, `x="61" y="39" width="18" height="18" rx="4.5" fill="#ffb347"`) {
		t.Errorf("favicon %s", svg)
	}
	if strings.Contains(faviconSVG(TileCounts{Current: 3}), "#ffb347") {
		t.Error("warm cell with nothing to attend to")
	}
}
