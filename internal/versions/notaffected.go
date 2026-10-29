// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"slices"
	"strings"

	"github.com/pipozzz/goliash/internal/store"
)

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
	case a.VEX != nil && (a.VEX.Status == "not_affected" || a.VEX.Status == "fixed"):
		why := vexJustifications[a.VEX.Justification]
		if why == "" {
			why = a.VEX.Impact
		}
		if a.VEX.Status == "fixed" {
			why = "fixed in this image"
		}
		if why == "" {
			why = "not affected"
		}
		by := "the image's publisher"
		if a.VEX.Author != "" {
			by = a.VEX.Author
		}
		return a.Package + ": " + why + ", says " + by + " (VEX)"
	case kernelPackage(a.Package):
		name, _ := splitPackage(a.Package)
		return name + ": a kernel package in the image; the container runs on its host's kernel, which is patched with the nodes"
	}
	return ""
}

// vexFor finds the statement of an image's VEX document about a vulnerability (any of its IDs) in a package: one
// about that package, else one about the whole image.
func vexFor(list []store.VEXStatement, ids []string, purl string) *store.VEXStatement {
	var whole *store.VEXStatement
	for i, s := range list {
		if !slices.Contains(ids, s.Vulnerability) {
			continue
		}
		if len(s.Packages) == 0 {
			whole = &list[i]
			continue
		}
		for _, p := range s.Packages {
			if samePackage(p, purl) {
				return &list[i]
			}
		}
	}
	return whole
}

// samePackage reports whether two purls name the same package: type and name, the version and qualifiers aside
// (a statement may name the package without its version).
func samePackage(a, b string) bool {
	strip := func(p string) string {
		p, _, _ = strings.Cut(p, "?")
		p, _, _ = strings.Cut(p, "@")
		return strings.ToLower(p)
	}
	return strip(a) == strip(b)
}

// vexJustifications say VEX's justifications as people would.
var vexJustifications = map[string]string{
	"component_not_present":                             "the component is not in the image",
	"vulnerable_code_not_present":                       "the vulnerable code is not in the image",
	"vulnerable_code_not_in_execute_path":               "the vulnerable code is never run",
	"vulnerable_code_cannot_be_controlled_by_adversary": "an attacker cannot reach the vulnerable code",
	"inline_mitigations_already_exist":                  "the image prevents it another way",
}
