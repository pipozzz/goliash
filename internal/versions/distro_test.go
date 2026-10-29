// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
	"testing"
)

func TestNewerReleaseFix(t *testing.T) {
	// CVE-2023-44487 in Debian: no fix in 12, fixed in 13 and 14.
	fixes := map[string][]string{"Debian:13|nginx": {"1.24.0-2"}, "Debian:14|nginx": {"1.24.0-2"}, "Debian:13|other": {"9"}}
	n, cur, ok := newerReleaseFix("pkg:deb/debian/nginx@1.22.1-9?os_distro=bookworm&os_name=debian&os_version=12", fixes)
	if !ok || n.Label() != "Debian 13 (trixie)" || n.Fix != "1.24.0-2" || cur != "Debian 12 (bookworm)" ||
		!strings.Contains(n.Advice(cur), "Debian 12 (bookworm) has no fix; Debian 13 (trixie) fixes it in nginx 1.24.0-2: rebuild on a trixie base image") {
		t.Errorf("debian: %+v %s %v", n, cur, ok)
	}
	// Debian 14 is testing: not a release to move to.
	if n, _, ok := newerReleaseFix("pkg:deb/debian/mariadb@1:11.8.6-0+deb13u1?os_version=13", map[string][]string{"Debian:14|mariadb": {"1:11.8.7-1"}}); ok {
		t.Errorf("testing offered: %+v", n)
	}
	// An Ubuntu interim release is not one either.
	if _, _, ok := newerReleaseFix("pkg:deb/ubuntu/curl@7.81.0-1?os_version=22.04", map[string][]string{"Ubuntu:25.10|curl": {"8"}}); ok {
		t.Error("interim Ubuntu offered")
	}
	// Already on the newest: nothing newer.
	if _, _, ok := newerReleaseFix("pkg:deb/debian/nginx@1.26.3-3?os_name=debian&os_version=14", fixes); ok {
		t.Error("14 has a newer release")
	}
	// Alpine and Ubuntu; Ubuntu Pro is not a release to move to.
	n, _, ok = newerReleaseFix("pkg:apk/alpine/busybox@1.36.1-r29?os_version=3.20.3", map[string][]string{"Alpine:v3.22|busybox": {"1.37.0-r18"}, "Alpine:v3.21|busybox": {"1.37.0-r12"}})
	if !ok || n.Label() != "Alpine 3.21" || n.Fix != "1.37.0-r12" {
		t.Errorf("alpine: %+v", n)
	}
	if _, _, ok := newerReleaseFix("pkg:deb/ubuntu/curl@7.81.0-1?os_version=22.04", map[string][]string{"Ubuntu:Pro:24.04:LTS|curl": {"8"}}); ok {
		t.Error("Ubuntu Pro offered")
	}
	n, _, ok = newerReleaseFix("pkg:deb/ubuntu/curl@7.81.0-1?os_version=22.04", map[string][]string{"Ubuntu:24.04:LTS|curl": {"8.5.0-2ubuntu10.4"}})
	if !ok || n.Label() != "Ubuntu 24.04 (noble)" {
		t.Errorf("ubuntu: %+v", n)
	}
	// Not a distribution package.
	if _, _, ok := newerReleaseFix("pkg:npm/lodash@4.17.15", fixes); ok {
		t.Error("npm has releases")
	}
}

func TestBaseOn(t *testing.T) {
	fixes := map[string][]string{"Debian:13|nginx": {"1.24.0-2"}}
	n, cur, _ := newerReleaseFix("pkg:deb/debian/nginx@1.22.1-9?os_version=12", fixes)
	for base, want := range map[string]string{
		"python:3.15-bookworm":                "python:3.15-trixie",
		"python:3.15-slim-bookworm@sha256:ab": "python:3.15-slim-trixie",
		"debian:bookworm-slim":                "debian:trixie-slim",
		"python:3.15":                         "",
		"ghcr.io/acme/base:1":                 "",
	} {
		if got := n.BaseOn(base); got != want {
			t.Errorf("%s: %q, want %q", base, got, want)
		}
	}
	if a := n.AdviceOn(cur, "python:3.15-bookworm"); !strings.Contains(a, "rebuild on python:3.15-trixie instead of python:3.15-bookworm") {
		t.Errorf("advice: %s", a)
	}
	if a := n.AdviceOn(cur, ""); a != n.Advice(cur) {
		t.Errorf("unknown base: %s", a)
	}
	al, _, _ := newerReleaseFix("pkg:apk/alpine/busybox@1.36.1-r29?os_version=3.20.3", map[string][]string{"Alpine:v3.21|busybox": {"1.37.0-r12"}})
	if got := al.BaseOn("node:22-alpine3.20"); got != "node:22-alpine3.21" {
		t.Errorf("alpine: %q", got)
	}
}
