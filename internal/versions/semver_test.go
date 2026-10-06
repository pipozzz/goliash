// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := map[string]Version{
		"1.27.2":           {Parts: []int{1, 27, 2}},
		"v1.31.2":          {Parts: []int{1, 31, 2}},
		"15.6-alpine":      {Parts: []int{15, 6}, Variant: "alpine"},
		"2.0.0-rc.1":       {Parts: []int{2, 0, 0}, Pre: "rc.1"},
		"8.0.0-beta2-slim": {Parts: []int{8, 0, 0}, Pre: "beta2", Variant: "slim"},
		"1.27.2-bookworm":  {Parts: []int{1, 27, 2}, Variant: "bookworm"},
		"7":                {Parts: []int{7}},
		"1.2.3.4":          {Parts: []int{1, 2, 3, 4}},
	}
	for in, want := range cases {
		got, ok := ParseVersion(in)
		if !ok || got.Pre != want.Pre || got.Variant != want.Variant || len(got.Parts) != len(want.Parts) {
			t.Errorf("ParseVersion(%q) = %+v, %v", in, got, ok)
			continue
		}
		for i := range want.Parts {
			if got.Parts[i] != want.Parts[i] {
				t.Errorf("ParseVersion(%q) parts %v", in, got.Parts)
			}
		}
	}
	for _, bad := range []string{"latest", "stable", "sha-1a2b3c", "main", "20240101", "sha256-abc.sig", ""} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("%q parsed as a version", bad)
		}
	}
}

func TestCompareAndJump(t *testing.T) {
	order := []string{"1.0.0-alpha", "1.0.0-alpha.2", "1.0.0-beta", "1.0.0-rc.1", "1.0.0-rc.2", "1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	for i := 1; i < len(order); i++ {
		a, _ := ParseVersion(order[i-1])
		b, _ := ParseVersion(order[i])
		if a.Compare(b) != -1 || b.Compare(a) != 1 {
			t.Errorf("%s should sort before %s", order[i-1], order[i])
		}
	}
	jump := func(a, b string) Jump {
		va, _ := ParseVersion(a)
		vb, _ := ParseVersion(b)
		return va.JumpTo(vb)
	}
	if jump("1.4.2", "1.4.3") != JumpPatch || jump("1.4.2", "1.6.0") != JumpMinor || jump("1.4.2", "2.0.0") != JumpMajor ||
		jump("1.4.2", "1.4.2") != JumpNone || jump("1.5.0", "1.4.9") != JumpNone {
		t.Fatal("jump sizes wrong")
	}
}

var nginxTags = strings.Fields(`latest 1.27 1.27.1 1.27.2 1.27.3 1.27.3-alpine 1.28.0 1.28.0-alpine 1.29.0-rc.1 mainline
	stable 1.26.2 1.29 sha256-abc.sig trixie`)

func TestLatest(t *testing.T) {
	cases := []struct {
		name, running string
		policy        Policy
		latest, any   string
	}{
		{"default: same shape", "1.27.2", Policy{}, "1.28.0", "1.28.0"},
		{"variant stays variant", "1.27.3-alpine", Policy{}, "1.28.0-alpine", "1.28.0-alpine"},
		{"two-part floating tag", "1.27", Policy{}, "1.29", "1.29"},
		{"prerelease opt-in", "1.27.2", Policy{Prerelease: true}, "1.29.0-rc.1", "1.29.0-rc.1"},
		{"tag filter", "1.27.2", Policy{TagFilter: `^1\.27\.\d+$`}, "1.27.3", "1.27.3"},
		{"pin major", "1.27.2", Policy{PinMajor: ptr(1)}, "1.28.0", "1.28.0"},
	}
	for _, c := range cases {
		u := Latest(nginxTags, c.running, c.policy)
		if u.Latest.Raw != c.latest || u.LatestAny.Raw != c.any {
			t.Errorf("%s: latest=%q any=%q, want %q %q", c.name, u.Latest.Raw, u.LatestAny.Raw, c.latest, c.any)
		}
	}

	pg := strings.Fields("15.5 15.6 15.7 16.2 16.3 17.0")
	u := Latest(pg, "15.6", Policy{PinMajor: ptr(15)})
	if u.Latest.Raw != "15.7" || u.LatestAny.Raw != "17.0" {
		t.Fatalf("postgres pin: %+v", u)
	}
	if j, lag := Lagging("15.6", u, Policy{PinMajor: ptr(15), Track: JumpMinor}); !lag || j != JumpMinor {
		t.Fatalf("15.6 -> 15.7 is a minor step for two-part tags and should alert: %s %v", j, lag)
	}
	if _, lag := Lagging("15.7", Latest(pg, "15.7", Policy{PinMajor: ptr(15)}), Policy{PinMajor: ptr(15)}); lag {
		t.Fatal("up to date within the pin, newer majors must not alert")
	}
	if j, lag := Lagging("1.27.2", Latest(nginxTags, "1.27.2", Policy{}), Policy{Track: JumpMinor}); !lag || j != JumpMinor {
		t.Fatalf("1.27.2 -> 1.28.0 with track minor: %s %v", j, lag)
	}
	if _, lag := Lagging("1.27.2", Latest(nginxTags, "1.27.2", Policy{TagFilter: `^1\.27\.\d+$`}), Policy{Track: JumpMinor}); lag {
		t.Fatal("patch lag must not alert when tracking minor")
	}
}

func TestParsePolicy(t *testing.T) {
	p, err := ParsePolicy([]byte(`{"tag_filter":"^\\d+\\.\\d+$","track":"minor","pin_major":15,"prerelease":false}`))
	if err != nil || p.Track != JumpMinor || *p.PinMajor != 15 {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := ParsePolicy([]byte(`{"track":"huge"}`)); err == nil {
		t.Fatal("bad track accepted")
	}
	if _, err := ParsePolicy([]byte(`{"tag_filter":"("}`)); err == nil {
		t.Fatal("bad regexp accepted")
	}
	if p, err := ParsePolicy(nil); err != nil || p.Track != JumpNone {
		t.Fatalf("empty: %+v %v", p, err)
	}
}

func ptr[T any](v T) *T { return &v }

// Tags that are no version name a flavour ("alpine", "pg18") or a line of plain
// releases ("latest"); "testing" builds are not releases.
func TestLatestForWordTags(t *testing.T) {
	for _, c := range []struct {
		running string
		tags    string
		want    string
	}{
		{"alpine", "8 8.2.1 8.2.1-alpine 8.2.0-alpine 8.2.1-alpine3.22 alpine latest", "8.2.1-alpine"},
		{"pg18", "0.8.7-pg13 0.8.7-pg17 0.8.1-pg18 0.8.0-pg18 pg18", "0.8.1-pg18"},
		{"latest", "4.104.0 4.103.2 v99.0.0-testing 4.104.0-ubuntu latest", "4.104.0"},
		{"stable", "1.27.2 1.28.0 1.29.0-rc.1 stable mainline", "1.28.0"},
	} {
		u := Latest(strings.Fields(c.tags), c.running, Policy{})
		if !u.HasLatest || u.Latest.Raw != c.want {
			t.Errorf("%s: latest %q, want %q", c.running, u.Latest.Raw, c.want)
		}
	}
	if v, _ := ParseVersion("v99.0.0-testing"); v.Pre != "testing" {
		t.Errorf("testing is a prerelease: %+v", v)
	}
	if got := strings.Join(movingCandidates("alpine", strings.Fields("8.2.1 8.2.1-alpine 8.2.0-alpine"), 4), " "); got != "8.2.1-alpine 8.2.0-alpine" {
		t.Errorf("moving alpine candidates %q", got)
	}
}
