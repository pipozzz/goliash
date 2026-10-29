// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import "strings"

// A container image may carry the kernel's packages (linux-libc-dev comes with build-essential, kernel-headers with
// gcc), and every kernel CVE matches them. None of that code runs: a container runs on its host's kernel. They are
// left out of an image's findings; the host's kernel is patched with the nodes.

// kernelPackages are kernel packages by exact name, kernelPrefixes by the start of it.
var (
	kernelPackages = map[string]bool{
		"linux-libc-dev": true, "linux-headers": true, "linux-api-headers": true, "linux-lts": true, "linux-virt": true,
		"linux-firmware": true, "kernel": true, "kernel-headers": true, "kernel-devel": true, "kernel-core": true,
		"kernel-default": true, "kernel-default-devel": true, "kernel-firmware": true, "linux-tools-common": true,
	}
	kernelPrefixes = []string{
		"linux-headers-", "linux-image-", "linux-modules-", "linux-kbuild-", "linux-compiler-", "linux-tools-",
		"linux-support-", "kernel-modules", "kernel-uek", "kernel-rt", "kernel-64k", "kernel-devel-",
	}
)

// kernelPackage reports whether a package ("linux-libc-dev 6.1.106-3") is the kernel's.
func kernelPackage(pkg string) bool {
	name, _ := splitPackage(pkg)
	if kernelPackages[name] {
		return true
	}
	for _, p := range kernelPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// autoNotAffected says why Goliash leaves an affected package out of a finding by itself, "" when it does not.
func autoNotAffected(a Affected) string {
	switch {
	case a.Upstream != nil && a.Upstream.Sure:
		return a.Upstream.Reason(a.Package) + " (NVD)"
	case kernelPackage(a.Package):
		name, _ := splitPackage(a.Package)
		return name + ": a kernel package in the image; the container runs on its host's kernel, which is patched with the nodes"
	}
	return ""
}
