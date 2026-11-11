// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import "testing"

func TestKernelPackage(t *testing.T) {
	for pkg, want := range map[string]bool{
		"linux-libc-dev 6.1.106-3": true, "linux-headers-6.1.0-25-amd64 6.1.106-3": true, "kernel-headers 5.14.0-427.el9": true,
		"linux-headers 6.6-r0": true, "linux-image-amd64 6.1.106-3": true, "kernel-devel-matched 5.14.0": true,
		"libc6 2.36-9": false, "linux-pam 1.5.2": false, "util-linux 2.38": false, "kernelshark 2.2": false, "nginx 1.27.1": false,
	} {
		if got := kernelPackage(pkg); got != want {
			t.Errorf("%s: %v", pkg, got)
		}
	}
}
