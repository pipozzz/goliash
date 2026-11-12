// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import "testing"

// A server's vulnerability says which server, and that a newer version fixes it where the version is not known.
func TestDescribeServerVulnerability(t *testing.T) {
	got := Describe(Item{
		Type: "vulnerability", Service: "ubuntu", Environment: "prod", Target: "mail-01",
		From: "linux-image-6.17.0-1010-aws 6.17.0-1010.10", To: "a newer version", Note: "CVE-2026-1234 exploited",
	})
	want := "ubuntu @ prod: CVE-2026-1234 (exploited) in linux-image-6.17.0-1010-aws 6.17.0-1010.10 on mail-01, fixed in a newer version"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
