// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// An image is built on an operating system release (Debian 11, Alpine 3.18), and once that release reaches its end
// of life its packages get no more security fixes: the image's vulnerabilities only grow, and no upgrade of the
// application fixes them. Goliash reads the release from the packages of the image (the package URLs' os_version,
// or the distro an SBOM tool wrote) and its end of life from endoflife.date.

// ImageOS is the operating system an image is built on.
type ImageOS struct {
	Distro  string // debian, ubuntu, alpine, rhel, rocky, almalinux, amzn
	Version string // 12, 22.04, 3.20.3, 9.4
}

// osProducts are the endoflife.date products of the distributions, by package URL namespace.
var osProducts = map[string]string{
	"debian": "debian", "ubuntu": "ubuntu", "alpine": "alpine-linux", "redhat": "rhel", "rhel": "rhel",
	"rocky": "rocky-linux", "almalinux": "almalinux", "amzn": "amazon-linux", "amazon": "amazon-linux",
}

// osNames are how the distributions are named to people.
var osNames = map[string]string{
	"debian": "Debian", "ubuntu": "Ubuntu", "alpine": "Alpine", "redhat": "RHEL", "rhel": "RHEL",
	"rocky": "Rocky Linux", "almalinux": "AlmaLinux", "amzn": "Amazon Linux", "amazon": "Amazon Linux",
}

// String is the release as people name it: "Debian 11 (bullseye)", "Alpine 3.18".
func (o ImageOS) String() string {
	s := osNames[o.Distro] + " " + o.Version
	if o.Distro == "debian" {
		if n := debianNames[o.Version]; n != "" {
			s += " (" + n + ")"
		}
	}
	return s
}

// OSOf reads the operating system release an image is built on from its package URLs; ok is false when none says.
func OSOf(purls []string) (ImageOS, bool) {
	for _, p := range purls {
		typ, rest, found := strings.Cut(strings.TrimPrefix(p, "pkg:"), "/")
		if !found || (typ != "deb" && typ != "apk" && typ != "rpm") {
			continue
		}
		distro := strings.ToLower(strings.SplitN(rest, "/", 2)[0])
		if osProducts[distro] == "" {
			continue
		}
		_, query, _ := strings.Cut(rest, "?")
		qs, _ := url.ParseQuery(query)
		version := qs.Get("os_version")
		if version == "" {
			version = qs.Get("distro_version")
		}
		if version == "" {
			if d := qs.Get("distro"); d != "" { // rhel-9.4, debian-12, alpine-3.20.3
				version = d[strings.LastIndex(d, "-")+1:]
			}
		}
		if _, ok := ParseVersion(version); !ok {
			continue
		}
		if distro == "debian" {
			version = strings.SplitN(version, ".", 2)[0] // 12.7 is Debian 12
		}
		return ImageOS{Distro: distro, Version: version}, true
	}
	return ImageOS{}, false
}

// sharedEOL is the endoflife.date client the server set, for what reads end of life outside a checker.
var sharedEOL atomic.Pointer[EOL]

// OSCycle is the release cycle of an image's operating system on endoflife.date, and the newest one still supported;
// it waits on nothing: what is not cached yet is fetched in the background, for the next time.
func OSCycle(ctx context.Context, o ImageOS) (cycle EOLCycle, newest string, ok bool) {
	e := sharedEOL.Load()
	if e == nil {
		return cycle, "", false
	}
	cycles, err := e.Cycles(CacheOnly(ctx), osProducts[o.Distro])
	if err != nil {
		return cycle, "", false
	}
	if cycle, ok = CycleFor(o.Version, cycles); !ok {
		return cycle, "", false
	}
	if o.Distro == "debian" || o.Distro == "alpine" { // Ubuntu's newest may be no LTS; RHEL's are paid for
		for _, c := range cycles {
			if !c.IsEOL {
				newest = c.Name // newest first
				break
			}
		}
	}
	return cycle, newest, true
}

// EOLOperatingSystems finds running images built on an operating system release past its end of life (a warning),
// or within eolWarning of it (info).
func EOLOperatingSystems(ctx context.Context, active []store.Instance, sboms map[string]store.ImageSBOM, envName, targetName map[string]string, now time.Time) []Finding {
	byImage := map[string]*Finding{}
	var order []string
	for _, i := range active {
		if i.Digest == "" {
			continue
		}
		ref := ParseImage(i.Image)
		key := ref.Repo() + ":" + ref.Tag
		f, seen := byImage[key]
		if !seen {
			byImage[key] = nil
			o, ok := OSOf(sboms[ref.Repo()+"@"+i.Digest].Purls)
			if !ok {
				continue
			}
			cy, newest, ok := OSCycle(ctx, o)
			if !ok || cy.EOLFrom.IsZero() && !cy.IsEOL {
				continue
			}
			o.Version = cy.Name // Alpine 3.21, not 3.21.2: the release, not its point release
			sev, detail := "", ""
			switch {
			case cy.IsEOL || !cy.EOLFrom.After(now):
				sev, detail = "warning", "built on "+o.String()+", past its end of life"
				if !cy.EOLFrom.IsZero() {
					detail += " since " + cy.EOLFrom.Format("Jan 2006")
				}
				detail += ": its packages get no more security fixes"
			case cy.EOLFrom.Sub(now) <= eolWarning:
				sev, detail = "info", "built on "+o.String()+", whose end of life is "+cy.EOLFrom.Format("Jan 2, 2006")+": its packages get no security fixes after"
			default:
				continue
			}
			if newest != "" && newest != cy.Name {
				detail += "; rebuild on " + ImageOS{Distro: o.Distro, Version: newest}.String()
			} else {
				detail += "; rebuild on a supported release"
			}
			f = &Finding{
				Kind: "eol-os", Severity: sev, Detail: detail,
				Image: strings.TrimPrefix(strings.TrimPrefix(key, "docker.io/library/"), "docker.io/"),
			}
			byImage[key] = f
			order = append(order, key)
		}
		if f != nil {
			f.Where = append(f.Where, envName[i.EnvironmentID]+" / "+targetName[i.TargetID]+" / "+i.WorkloadName+" / "+i.ContainerName)
		}
	}
	out := make([]Finding, 0, len(order))
	for _, k := range order {
		out = append(out, *byImage[k])
	}
	return out
}

// eolOSOf names the operating system release a package comes from when that release is past its end of life,
// remembering the answer per release in memo.
func eolOSOf(ctx context.Context, purl string, memo map[ImageOS]string) string {
	o, ok := OSOf([]string{purl})
	if !ok {
		return ""
	}
	if name, done := memo[o]; done {
		return name
	}
	name := ""
	if cy, _, ok := OSCycle(ctx, o); ok && (cy.IsEOL || !cy.EOLFrom.IsZero() && !cy.EOLFrom.After(time.Now())) {
		name = ImageOS{Distro: o.Distro, Version: cy.Name}.String()
	}
	memo[o] = name
	return name
}

// GoOf is the oldest Go release the Go programs of an image were built with ("1.22.3"), from its package URLs
// (pkg:golang/stdlib@1.22.3); ok is false when it has none.
func GoOf(purls []string) (string, bool) {
	var oldest Version
	found := ""
	for _, p := range purls {
		v, ok := strings.CutPrefix(p, "pkg:golang/stdlib@")
		if !ok {
			continue
		}
		v, _, _ = strings.Cut(v, "?")
		pv, ok := ParseVersion(v)
		if !ok || len(pv.Parts) < 2 {
			continue
		}
		if found == "" || pv.Compare(oldest) < 0 {
			oldest, found = pv, v
		}
	}
	return found, found != ""
}

// EOLRuntimes finds running images with Go programs built with a Go release past its end of life: the standard
// library they carry (net/http, crypto/tls) gets no more security fixes. Go supports its two newest releases.
func EOLRuntimes(ctx context.Context, active []store.Instance, sboms map[string]store.ImageSBOM, envName, targetName map[string]string, now time.Time) []Finding {
	e := sharedEOL.Load()
	if e == nil {
		return nil
	}
	cycles, err := e.Cycles(CacheOnly(ctx), "go")
	if err != nil {
		return nil
	}
	newest := ""
	for _, c := range cycles {
		if !c.IsEOL && (c.EOLFrom.IsZero() || c.EOLFrom.After(now)) {
			newest = c.Name
			break
		}
	}
	byImage := map[string]*Finding{}
	var order []string
	for _, i := range active {
		if i.Digest == "" {
			continue
		}
		ref := ParseImage(i.Image)
		key := ref.Repo() + ":" + ref.Tag
		f, seen := byImage[key]
		if !seen {
			byImage[key] = nil
			v, ok := GoOf(sboms[ref.Repo()+"@"+i.Digest].Purls)
			if !ok {
				continue
			}
			cy, ok := CycleFor(v, cycles)
			if !ok || !cy.IsEOL && (cy.EOLFrom.IsZero() || cy.EOLFrom.After(now)) {
				continue
			}
			detail := "has Go programs built with Go " + v + ", past its end of life"
			if !cy.EOLFrom.IsZero() {
				detail += " since " + cy.EOLFrom.Format("Jan 2006")
			}
			detail += ": the standard library they carry gets no more security fixes; "
			if newest != "" {
				detail += "rebuild with Go " + newest + ", or move to a release built with a supported Go"
			} else {
				detail += "rebuild with a supported Go, or move to a release built with one"
			}
			f = &Finding{
				Kind: "eol-runtime", Severity: "warning", Detail: detail,
				Image: strings.TrimPrefix(strings.TrimPrefix(key, "docker.io/library/"), "docker.io/"),
			}
			byImage[key] = f
			order = append(order, key)
		}
		if f != nil {
			f.Where = append(f.Where, envName[i.EnvironmentID]+" / "+targetName[i.TargetID]+" / "+i.WorkloadName+" / "+i.ContainerName)
		}
	}
	out := make([]Finding, 0, len(order))
	for _, k := range order {
		out = append(out, *byImage[k])
	}
	return out
}

// GoPastEOL reports whether the Go release v ("1.25.7") is past its end of life; false when that is not known yet.
func GoPastEOL(ctx context.Context, v string) bool {
	e := sharedEOL.Load()
	if e == nil {
		return false
	}
	cycles, err := e.Cycles(CacheOnly(ctx), "go")
	if err != nil {
		return false
	}
	cy, ok := CycleFor(v, cycles)
	return ok && (cy.IsEOL || !cy.EOLFrom.IsZero() && !cy.EOLFrom.After(time.Now()))
}
