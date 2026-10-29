// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
	"sync"
)

// A distribution often fixes a vulnerability in its next release and not in the one an image is built on: Debian
// 13 has a fixed nginx while Debian 12 has none. OSV's advisory names the fixed version per release; the newest
// fix there is moving the image to a base on that release.

// debianStable is Debian's newest stable release: a newer one is testing (forky) or unstable, not a base to move a
// production image to. Raise it when Debian releases the next.
const debianStable = 13

// debianNames are Debian's releases by number, as image tags name them.
var debianNames = map[string]string{"10": "buster", "11": "bullseye", "12": "bookworm", "13": "trixie", "14": "forky", "15": "duke"}

// distroRelease reads an OSV ecosystem as a distribution and its release: "Debian:13" is Debian 13, "Ubuntu:24.04:LTS"
// Ubuntu 24.04, "Alpine:v3.21" Alpine 3.21. Ubuntu Pro and other ecosystems are not releases to move to.
func distroRelease(eco string) (distro, name string, release Version, ok bool) {
	if v, hit := releaseMemo.Load(eco); hit {
		r := v.(distroRel)
		return r.distro, r.name, r.release, r.ok
	}
	distro, name, release, ok = parseDistroRelease(eco)
	releaseMemo.Store(eco, distroRel{distro, name, release, ok}) // a few hundred ecosystems at most
	return distro, name, release, ok
}

var releaseMemo sync.Map

type distroRel struct {
	distro, name string
	release      Version
	ok           bool
}

func parseDistroRelease(eco string) (distro, name string, release Version, ok bool) {
	parts := strings.Split(eco, ":")
	if len(parts) < 2 || (parts[0] != "Debian" && parts[0] != "Alpine" && parts[0] != "Ubuntu") || parts[1] == "Pro" {
		return "", "", Version{}, false
	}
	name = strings.TrimPrefix(parts[1], "v")
	release, ok = ParseVersion(name)
	return parts[0], name, release, ok
}

// movable reports whether a release is one to move a production image to: a stable Debian, an Ubuntu LTS.
func movable(eco string) bool {
	parts := strings.Split(eco, ":")
	switch parts[0] {
	case "Debian":
		v, ok := ParseVersion(parts[1])
		return ok && len(v.Parts) > 0 && v.Parts[0] <= debianStable
	case "Ubuntu":
		return len(parts) > 2 && parts[2] == "LTS"
	}
	return true
}

// NewerRelease is a newer release of an image's distribution that fixes a vulnerability its own release does not.
type NewerRelease struct {
	Distro, Release string // Debian, 13
	Name            string // trixie, for Debian
	Package, Fix    string // the source package and the version that fixes it there
	// FromRelease and FromName are the image's own release: 12 and bookworm.
	FromRelease, FromName string
}

// ubuntuNames are Ubuntu's releases by version, as image tags name them.
var ubuntuNames = map[string]string{"20.04": "focal", "22.04": "jammy", "24.04": "noble", "26.04": "resolute"}

// BaseOn is the image to build on instead of base for the newer release: python:3.15-bookworm becomes
// python:3.15-trixie, node:22-alpine3.20 node:22-alpine3.21, ubuntu:jammy ubuntu:noble. "" when the tag does not
// name the release.
func (n NewerRelease) BaseOn(base string) string {
	base, _, _ = strings.Cut(base, "@")
	repo, tag, ok := strings.Cut(base, ":")
	if !ok {
		return ""
	}
	from, to := n.FromName, n.Name
	switch n.Distro {
	case "Alpine":
		from, to = "alpine"+n.FromRelease, "alpine"+n.Release
	case "Ubuntu":
		if from == "" || to == "" {
			from, to = n.FromRelease, n.Release
		}
	}
	if from == "" || to == "" || !strings.Contains(tag, from) {
		return ""
	}
	return repo + ":" + strings.Replace(tag, from, to, 1)
}

// AdviceOn says what to do, naming the image to build on when the image's base is known.
func (n NewerRelease) AdviceOn(current, base string) string {
	if to := n.BaseOn(base); to != "" {
		return current + " has no fix; " + n.Label() + " fixes it in " + n.Package + " " + n.Fix + ": rebuild on " + to +
			" instead of " + base + " (its slim variant also leaves out the build tools a runtime image does not need)."
	}
	return n.Advice(current)
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
	switch {
	case n.Distro == "Debian" && n.Name != "":
		base = "a " + n.Name + " base image (debian:" + n.Name + "-slim, or the " + n.Name + " variant of the image it is built on)"
	case n.Distro == "Ubuntu" && n.Name != "":
		base = "a " + n.Name + " base image (ubuntu:" + n.Name + ", or the " + n.Name + " variant of the image it is built on)"
	case n.Distro == "Alpine":
		base = "an Alpine " + n.Release + " base image (alpine:" + n.Release + ", or the alpine" + n.Release + " variant of the image it is built on)"
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
		if !ok || d != distro || name != q.Package.Name || len(fs) == 0 || v.Compare(cur) <= 0 || !movable(eco) {
			continue
		}
		if best.Distro == "" || v.Compare(bestV) < 0 {
			best, bestV = NewerRelease{Distro: d, Release: rel, Package: name, Fix: fs[len(fs)-1]}, v
		}
	}
	if best.Distro == "" {
		return NewerRelease{}, "", false
	}
	best.FromRelease = curName
	switch distro {
	case "Debian":
		best.Name, best.FromName = debianNames[best.Release], debianNames[curName]
	case "Ubuntu":
		best.Name, best.FromName = ubuntuNames[best.Release], ubuntuNames[curName]
	}
	current := distro + " " + curName
	if distro == "Debian" && debianNames[curName] != "" {
		current += " (" + debianNames[curName] + ")"
	}
	return best, current, true
}
