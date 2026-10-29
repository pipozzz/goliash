// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
)

// A distribution often fixes a vulnerability in its next release and not in the one an image is built on: Debian
// 13 has a fixed nginx while Debian 12 has none. OSV's advisory names the fixed version per release; the newest
// fix there is moving the image to a base on that release.

// debianNames are Debian's releases by number, as image tags name them.
var debianNames = map[string]string{"10": "buster", "11": "bullseye", "12": "bookworm", "13": "trixie", "14": "forky", "15": "duke"}

// distroRelease reads an OSV ecosystem as a distribution and its release: "Debian:13" is Debian 13, "Ubuntu:24.04:LTS"
// Ubuntu 24.04, "Alpine:v3.21" Alpine 3.21. Ubuntu Pro and other ecosystems are not releases to move to.
func distroRelease(eco string) (distro, name string, release Version, ok bool) {
	parts := strings.Split(eco, ":")
	if len(parts) < 2 || (parts[0] != "Debian" && parts[0] != "Alpine" && parts[0] != "Ubuntu") || parts[1] == "Pro" {
		return "", "", Version{}, false
	}
	name = strings.TrimPrefix(parts[1], "v")
	release, ok = ParseVersion(name)
	return parts[0], name, release, ok
}

// NewerRelease is a newer release of an image's distribution that fixes a vulnerability its own release does not.
type NewerRelease struct {
	Distro, Release string // Debian, 13
	Name            string // trixie, for Debian
	Package, Fix    string // the source package and the version that fixes it there
}

// Label names the release: "Debian 13 (trixie)", "Alpine 3.21".
func (n NewerRelease) Label() string {
	l := n.Distro + " " + n.Release
	if n.Name != "" {
		l += " (" + n.Name + ")"
	}
	return l
}

// Advice says what to do, in a sentence.
func (n NewerRelease) Advice(current string) string {
	base := "a " + n.Distro + " " + n.Release + " base image"
	if n.Name != "" {
		base = "a " + n.Name + " base image (debian:" + n.Name + "-slim, or the " + n.Name + " variant of the image it is built on)"
	}
	return current + " has no fix; " + n.Label() + " fixes it in " + n.Package + " " + n.Fix + ": rebuild on " + base + "."
}

// newerReleaseFix finds the oldest newer release of purl's distribution that fixes a vulnerability, from the
// vulnerability's fixes by "ecosystem|package".
func newerReleaseFix(purl string, fixes map[string][]string) (NewerRelease, string, bool) {
	qs := osvQueries(purl)
	if len(qs) == 0 {
		return NewerRelease{}, "", false
	}
	q := qs[0]
	distro, curName, cur, ok := distroRelease(q.Package.Ecosystem)
	if !ok {
		return NewerRelease{}, "", false
	}
	var best NewerRelease
	var bestV Version
	for key, fs := range fixes {
		eco, name, _ := strings.Cut(key, "|")
		d, rel, v, ok := distroRelease(eco)
		if !ok || d != distro || name != q.Package.Name || len(fs) == 0 || v.Compare(cur) <= 0 {
			continue
		}
		if best.Distro == "" || v.Compare(bestV) < 0 {
			best, bestV = NewerRelease{Distro: d, Release: rel, Package: name, Fix: fs[len(fs)-1]}, v
		}
	}
	if best.Distro == "" {
		return NewerRelease{}, "", false
	}
	if distro == "Debian" {
		best.Name = debianNames[best.Release]
	}
	current := distro + " " + curName
	if distro == "Debian" && debianNames[curName] != "" {
		current += " (" + debianNames[curName] + ")"
	}
	return best, current, true
}
