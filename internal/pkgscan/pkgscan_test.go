// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package pkgscan

import (
	"slices"
	"testing"
)

func TestWantsSystem(t *testing.T) {
	sys := Scanner{System: true}
	for name, want := range map[string]bool{
		"usr/lib/python3/dist-packages/urllib3-1.26.12.dist-info/METADATA":          false, // the distribution's
		"usr/share/nodejs/lodash/node_modules/x/package.json":                       false,
		"usr/local/lib/python3.12/site-packages/requests-2.31.0.dist-info/METADATA": true,
		"usr/local/lib/node_modules/npm/node_modules/semver/package.json":           true,
		"app/node_modules/express/package.json":                                     true,
		"opt/app/lib/log4j-core-2.14.1.jar":                                         true,
		"app/README.md":                                                             false,
	} {
		if got := sys.Wants(name, 100, false) > 0; got != want {
			t.Errorf("%s: %v", name, got)
		}
	}
	if (&Scanner{}).Wants("lib/log4j-core.jar", 100, false) == 0 {
		t.Error("a Lambda zip's lib/ is not the system's")
	}
	if sys.Wants("usr/local/bin/server", MaxNested+1, true) != 0 {
		t.Error("an executable over the limit is read")
	}
}

func TestLockOnlyWithoutModules(t *testing.T) {
	var s Scanner
	s.Add("app/package-lock.json", []byte(`{"packages":{"node_modules/express":{"version":"4.18.2"}}}`))
	if got := s.Purls(); !slices.Equal(got, []string{"pkg:npm/express@4.18.2"}) {
		t.Errorf("lock alone: %v", got)
	}
	s.Add("app/node_modules/express/package.json", []byte(`{"name":"express","version":"4.19.0"}`))
	if got := s.Purls(); !slices.Equal(got, []string{"pkg:npm/express@4.19.0"}) {
		t.Errorf("with node_modules: %v", got)
	}
}
