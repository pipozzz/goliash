// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"net/url"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/store"
)

// LicenseClass is what a license asks of whoever ships or runs the software, from a compliance point of view.
type LicenseClass string

// License classes, most demanding first.
const (
	// NetworkCopyleft licenses (AGPL, SSPL, OSL…) reach software used over a network: running it as a service
	// can oblige you to publish your changes, or more.
	NetworkCopyleft LicenseClass = "network-copyleft"
	// SourceAvailable licenses (BUSL, Elastic, PolyForm, Commons Clause, non-commercial ones) are not open source:
	// they limit production, commercial or competing use.
	SourceAvailable LicenseClass = "source-available"
	// StrongCopyleft licenses (GPL, EUPL…) reach software you distribute together with the package.
	StrongCopyleft LicenseClass = "strong-copyleft"
	// WeakCopyleft licenses (LGPL, MPL, EPL…) reach changes to the package itself.
	WeakCopyleft LicenseClass = "weak-copyleft"
	// UnknownLicense is a package whose SBOM names no license, or one Goliash does not know.
	UnknownLicense LicenseClass = "unknown"
	// Permissive licenses (MIT, Apache, BSD…) ask for attribution only.
	Permissive LicenseClass = "permissive"
)

// LicenseClasses lists the classes, most demanding first.
var LicenseClasses = []LicenseClass{NetworkCopyleft, SourceAvailable, StrongCopyleft, WeakCopyleft, UnknownLicense, Permissive}

// rank orders classes by how much they ask: an OR of licenses lets you pick the least demanding, an AND binds
// you to the most demanding.
func (c LicenseClass) rank() int {
	switch c {
	case Permissive:
		return 0
	case WeakCopyleft:
		return 1
	case UnknownLicense:
		return 2
	case StrongCopyleft:
		return 3
	case SourceAvailable:
		return 4
	case NetworkCopyleft:
		return 5
	}
	return 2
}

// Attention tells whether a class needs a look before shipping: copyleft that reaches your code, source-available
// terms, and packages whose license is unknown.
func (c LicenseClass) Attention() bool {
	return c == NetworkCopyleft || c == SourceAvailable || c == StrongCopyleft || c == UnknownLicense
}

// licensePrefixes classify license IDs (SPDX, and the names Debian and Alpine use) by prefix, lower case and
// longest first where they overlap.
var licensePrefixes = []struct {
	prefix string
	class  LicenseClass
}{
	{"agpl", NetworkCopyleft},
	{"sspl", NetworkCopyleft},
	{"osl-", NetworkCopyleft},
	{"rpl-", NetworkCopyleft},
	{"cpal-", NetworkCopyleft},
	{"busl", SourceAvailable},
	{"elastic-", SourceAvailable},
	{"polyform", SourceAvailable},
	{"commons-clause", SourceAvailable},
	{"cc-by-nc", SourceAvailable},
	{"confluent-community", SourceAvailable},
	{"lgpl", WeakCopyleft},
	{"mpl", WeakCopyleft},
	{"epl", WeakCopyleft},
	{"cddl", WeakCopyleft},
	{"cpl-", WeakCopyleft},
	{"ms-rl", WeakCopyleft},
	{"eupl", StrongCopyleft},
	{"gpl", StrongCopyleft},
	{"cc-by-sa", StrongCopyleft},
	{"sleepycat", StrongCopyleft},
	{"qpl", StrongCopyleft},
	{"mit", Permissive},
	{"apache", Permissive},
	{"bsd", Permissive},
	{"0bsd", Permissive},
	{"isc", Permissive},
	{"zlib", Permissive},
	{"curl", Permissive},
	{"unlicense", Permissive},
	{"psf", Permissive},
	{"python", Permissive},
	{"x11", Permissive},
	{"bsl-1.0", Permissive},
	{"boost", Permissive},
	{"cc0", Permissive},
	{"cc-by-", Permissive},
	{"wtfpl", Permissive},
	{"openssl", Permissive},
	{"ssleay", Permissive},
	{"postgresql", Permissive},
	{"libpng", Permissive},
	{"libtiff", Permissive},
	{"ncsa", Permissive},
	{"artistic-2", Permissive},
	{"ruby", Permissive},
	{"icu", Permissive},
	{"unicode", Permissive},
	{"ftl", Permissive},
	{"ofl", Permissive},
	{"blueoak", Permissive},
	{"upl", Permissive},
	{"w3c", Permissive},
	{"expat", Permissive},
	{"public-domain", Permissive},
	{"publicdomain", Permissive},
	{"beerware", Permissive},
	{"ijg", Permissive},
	{"hpnd", Permissive},
	{"bzip2", Permissive},
	{"ms-pl", Permissive},
	{"afl-", Permissive},
	{"zpl", Permissive},
	{"vim", Permissive},
	{"imagemagick", Permissive},
	{"tcl", Permissive},
	{"info-zip", Permissive},
	{"json", Permissive},
	{"lpl", Permissive},
	{"eclipse-distribution", Permissive},
	{"edl-", Permissive},
	{"gfdl", WeakCopyleft},
}

// classifyID classifies one license ID, "GPL-2.0-only WITH Classpath-exception-2.0" included: a GPL with a
// linking exception asks no more than a weak copyleft.
func classifyID(id string) LicenseClass {
	id = strings.ToLower(strings.TrimSpace(id))
	base, exception, with := strings.Cut(id, " with ")
	base = strings.TrimSpace(base)
	best := ""
	class := UnknownLicense
	for _, p := range licensePrefixes {
		if strings.HasPrefix(base, p.prefix) && len(p.prefix) > len(best) {
			best, class = p.prefix, p.class
		}
	}
	if with && class == StrongCopyleft && strings.TrimSpace(exception) != "" {
		return WeakCopyleft
	}
	return class
}

// ClassifyLicense classifies an SPDX license expression: "MIT OR GPL-2.0" lets you pick MIT and is permissive;
// "MIT AND GPL-2.0" binds you to both and is strong copyleft. An empty expression is unknown.
func ClassifyLicense(expr string) LicenseClass {
	p := &licenseParser{tokens: licenseTokens(expr)}
	if len(p.tokens) == 0 {
		return UnknownLicense
	}
	return p.or()
}

type licenseParser struct {
	tokens []string
	pos    int
}

func licenseTokens(expr string) []string {
	expr = strings.ReplaceAll(strings.ReplaceAll(expr, "(", " ( "), ")", " ) ")
	var out []string
	fields := strings.Fields(expr)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		// "X WITH Y" is one license.
		if i+2 < len(fields) && strings.EqualFold(fields[i+1], "WITH") {
			f += " with " + fields[i+2]
			i += 2
		}
		out = append(out, f)
	}
	return out
}

func (p *licenseParser) peek() string {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return ""
}

func (p *licenseParser) or() LicenseClass {
	c := p.and()
	for strings.EqualFold(p.peek(), "OR") || p.peek() == "/" || p.peek() == "|" {
		p.pos++
		if alt := p.and(); alt.rank() < c.rank() {
			c = alt
		}
	}
	return c
}

func (p *licenseParser) and() LicenseClass {
	c := p.factor()
	for strings.EqualFold(p.peek(), "AND") || p.peek() == "," || p.peek() == "&" {
		p.pos++
		if more := p.factor(); more.rank() > c.rank() {
			c = more
		}
	}
	return c
}

func (p *licenseParser) factor() LicenseClass {
	t := p.peek()
	switch t {
	case "":
		return UnknownLicense
	case "(":
		p.pos++
		c := p.or()
		if p.peek() == ")" {
			p.pos++
		}
		return c
	}
	p.pos++
	return classifyID(t)
}

// osPackageTypes are the purl types of operating system packages: their licenses are the distribution's, and
// copyleft in them (glibc, bash) is the norm, not news.
var osPackageTypes = map[string]bool{"deb": true, "apk": true, "rpm": true, "alpm": true}

// LicensedPackage is a package in running images, with its license and where it runs.
type LicensedPackage struct {
	Purl, Name, Version string
	License             string // as the SBOM names it; empty when it names none
	Class               LicenseClass
	OS                  bool // an operating system package (deb, apk, rpm)
	Where               []LicenseUse
}

// LicenseUse is a service and environment a package runs in.
type LicenseUse struct {
	Service, ServiceID, Environment, EnvironmentID string
}

// LicenseReport is what the SBOMs of the running images say about licenses.
type LicenseReport struct {
	Packages []LicensedPackage // most demanding class first, then by name
	Images   int               // running images with a digest
	WithSBOM int               // of them, with an SBOM read
	// Counts are packages per class: application packages, and operating system ones apart.
	Counts, OSCounts map[LicenseClass]int
	// LicensesUnread are images whose SBOM was read before licenses were kept; they are read again soon.
	LicensesUnread int
}

// LoadLicenseReport builds the report for the running images, envID limiting it to one environment ("" for all).
func LoadLicenseReport(ctx context.Context, st *store.Store, sc store.Scope, envID string) (LicenseReport, error) {
	r := LicenseReport{Counts: map[LicenseClass]int{}, OSCounts: map[LicenseClass]int{}}
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return r, err
	}
	sboms, err := st.ListImageSBOMs(ctx, sc)
	if err != nil {
		return r, err
	}
	services, err := st.ListServices(ctx, sc)
	if err != nil {
		return r, err
	}
	envs, err := st.ListEnvironments(ctx, sc)
	if err != nil {
		return r, err
	}
	svcName, envName, envPos := map[string]string{}, map[string]string{}, map[string]int{}
	for _, s := range services {
		svcName[s.ID] = s.Name
	}
	for i, e := range envs {
		envName[e.ID], envPos[e.ID] = e.Name, i
	}
	byPurl := map[string]*LicensedPackage{}
	images := map[string]bool{}
	for _, in := range active {
		key := in.PackagesKey(ParseImage(in.Image).Repo())
		if key == "" || (envID != "" && in.EnvironmentID != envID) {
			continue
		}
		b := sboms[key]
		if !images[key] {
			images[key] = true
			r.Images++
			if len(b.Purls) > 0 {
				r.WithSBOM++
				if NeedsLicenses(b) {
					r.LicensesUnread++
				}
			}
		}
		name := svcName[in.ServiceID]
		if name == "" {
			name = in.WorkloadName
		}
		use := LicenseUse{Service: name, ServiceID: in.ServiceID, Environment: envName[in.EnvironmentID], EnvironmentID: in.EnvironmentID}
		for _, p := range b.Purls {
			lp := byPurl[p]
			if lp == nil {
				lic := b.Licenses[p]
				lp = &LicensedPackage{Purl: p, Name: purlPackage(p), Version: purlVersion(p), License: lic, Class: ClassifyLicense(lic), OS: osPackage(p)}
				byPurl[p] = lp
			} else if lp.License == "" && b.Licenses[p] != "" {
				lp.License = b.Licenses[p]
				lp.Class = ClassifyLicense(lp.License)
			}
			if !containsUse(lp.Where, use) {
				lp.Where = append(lp.Where, use)
			}
		}
	}
	for _, lp := range byPurl {
		sort.Slice(lp.Where, func(i, j int) bool {
			a, b := lp.Where[i], lp.Where[j]
			if a.Service != b.Service {
				return a.Service < b.Service
			}
			return envPos[a.EnvironmentID] < envPos[b.EnvironmentID]
		})
		if lp.OS {
			r.OSCounts[lp.Class]++
		} else {
			r.Counts[lp.Class]++
		}
		r.Packages = append(r.Packages, *lp)
	}
	sort.Slice(r.Packages, func(i, j int) bool {
		a, b := r.Packages[i], r.Packages[j]
		if a.Class.rank() != b.Class.rank() {
			return a.Class.rank() > b.Class.rank()
		}
		if a.OS != b.OS {
			return !a.OS
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Version < b.Version
	})
	return r, nil
}

func osPackage(purl string) bool {
	t, _, _ := strings.Cut(strings.TrimPrefix(purl, "pkg:"), "/")
	return osPackageTypes[t]
}

func containsUse(list []LicenseUse, u LicenseUse) bool {
	for _, x := range list {
		if x.ServiceID == u.ServiceID && x.EnvironmentID == u.EnvironmentID && x.Service == u.Service {
			return true
		}
	}
	return false
}

// LicenseMatches tells whether a package matches a search: its name, purl, license or a service it runs in.
func LicenseMatches(p LicensedPackage, q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(p.Purl), q) || strings.Contains(strings.ToLower(p.License), q) {
		return true
	}
	if u, err := url.PathUnescape(p.Purl); err == nil && strings.Contains(strings.ToLower(u), q) {
		return true
	}
	for _, w := range p.Where {
		if strings.EqualFold(w.Service, q) {
			return true
		}
	}
	return false
}

// PurlName is a package URL's name without its version: com.itextpdf/itextpdf, debian/openssl.
func PurlName(p string) string { return purlPackage(p) }

// PurlVersion is a package URL's version.
func PurlVersion(p string) string { return purlVersion(p) }

// OSPackage tells whether a package URL names an operating system package (deb, apk, rpm).
func OSPackage(p string) bool { return osPackage(p) }
