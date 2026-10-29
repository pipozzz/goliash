// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package pkgscan finds the application packages in a file tree (an image's layers, a Lambda function's zip) from
// their manifests, without running anything: npm (node_modules/*/package.json, else package-lock.json), PyPI
// (*.dist-info/METADATA, *.egg-info/PKG-INFO), Maven (pom.properties in jars, nested ones too), Go (the build
// information of executables), RubyGems (specifications/*.gemspec) and NuGet (*.deps.json). It reports package
// URLs, as an SBOM would.
package pkgscan

import (
	"archive/zip"
	"bufio"
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"path"
	"sort"
	"strings"
)

// Limits on what is read of one file.
const (
	MaxManifest = 2 << 20  // a package.json, METADATA, deps.json …
	MaxLock     = 64 << 20 // a package-lock.json
	MaxNested   = 64 << 20 // a jar, or an executable read for its Go build information
	maxDepth    = 2        // jars inside jars
)

// Scanner collects the packages of the files it is fed.
type Scanner struct {
	// System leaves out files under the directories an operating system's package manager owns (usr/lib,
	// usr/share, bin …): packages a distribution installed there are known as its own, with its patches, and
	// matching them against upstream advisories too would report what the distribution fixed.
	System bool

	found   map[string]bool
	modules bool     // node_modules seen: a lock file then only repeats them
	lock    []string // packages of package-lock.json files, used when there is no node_modules
}

// systemDirs are where a distribution's packages put files.
var systemDirs = []string{"usr/lib/", "usr/lib64/", "usr/share/", "usr/bin/", "usr/sbin/", "usr/libexec/", "lib/", "lib64/", "bin/", "sbin/", "etc/"}

// Wants says whether a file is one the scanner reads, and up to how many bytes; 0 when it does not want it.
func (s *Scanner) Wants(name string, size int64, executable bool) int64 {
	name = strings.TrimPrefix(path.Clean("/"+name), "/")
	if s.System {
		for _, d := range systemDirs {
			if strings.HasPrefix(name, d) {
				return 0
			}
		}
	}
	base := path.Base(name)
	limit := int64(0)
	switch {
	case base == "package.json" && strings.Contains(name, "node_modules/"):
		limit = MaxManifest
	case base == "package-lock.json" && !strings.Contains(name, "node_modules/"):
		limit = MaxLock
	case base == "METADATA" && strings.HasSuffix(path.Dir(name), ".dist-info"), base == "PKG-INFO" && strings.HasSuffix(path.Dir(name), ".egg-info"):
		limit = MaxManifest
	case base == "pom.properties" && strings.Contains(name, "META-INF/maven/"):
		limit = MaxManifest
	case strings.HasSuffix(base, ".jar"), strings.HasSuffix(base, ".war"), strings.HasSuffix(base, ".ear"):
		limit = MaxNested
	case strings.HasSuffix(base, ".gemspec") && strings.Contains(name, "/specifications/"):
		limit = MaxManifest // its name says it all, but it is small
	case strings.HasSuffix(base, ".deps.json"):
		limit = 16 << 20
	case executable && !strings.Contains(base, "."):
		limit = MaxNested
	}
	if limit == 0 || size > limit {
		return 0
	}
	return limit
}

func baseName(name string) string { return path.Base(name) }

// Add reads a file Wants wanted.
func (s *Scanner) Add(name string, b []byte) { s.add(name, b, 0) }

func (s *Scanner) add(name string, b []byte, depth int) {
	if s.found == nil {
		s.found = map[string]bool{}
	}
	name = strings.TrimPrefix(path.Clean("/"+name), "/")
	base := path.Base(name)
	put := func(ps ...string) {
		for _, p := range ps {
			if p != "" {
				s.found[p] = true
			}
		}
	}
	switch {
	case base == "package.json" && strings.Contains(name, "node_modules/"):
		s.modules = true
		put(npmPackage(name, b))
	case base == "package-lock.json":
		s.lock = append(s.lock, npmLock(b)...)
	case base == "METADATA", base == "PKG-INFO":
		put(pypiPackage(b))
	case base == "pom.properties":
		put(mavenPackage(b))
	case strings.HasSuffix(base, ".jar"), strings.HasSuffix(base, ".war"), strings.HasSuffix(base, ".ear"):
		if depth >= maxDepth {
			return
		}
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return
		}
		for _, f := range zr.File {
			if s.Wants(f.Name, int64(f.UncompressedSize64), false) == 0 { //nolint:gosec // sizes in a zip fit
				continue
			}
			if inner := Read(f, MaxNested); inner != nil {
				s.add(f.Name, inner, depth+1)
			}
		}
	case strings.HasSuffix(base, ".gemspec"):
		put(gemPackage(base))
	case strings.HasSuffix(base, ".deps.json"):
		put(nugetPackages(b)...)
	default:
		put(goPackages(b)...)
	}
}

// Purls returns the packages found, sorted.
func (s *Scanner) Purls() []string {
	set := map[string]bool{}
	for p := range s.found {
		set[p] = true
	}
	if !s.modules {
		for _, p := range s.lock {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Read reads a zip entry, up to limit bytes; nil when it is larger or unreadable.
func Read(f *zip.File, limit int64) []byte {
	if f.UncompressedSize64 > uint64(limit) { //nolint:gosec // limit is one of the positive constants above
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer func() { _ = rc.Close() }()
	b, err := readAll(rc, limit)
	if err != nil {
		return nil
	}
	return b
}

func npmName(name string) string {
	if strings.HasPrefix(name, "@") {
		return "%40" + name[1:]
	}
	return name
}

// npmPackage reads node_modules/<name>/package.json (or node_modules/@scope/<name>/…), not files deeper in a
// package.
func npmPackage(name string, b []byte) string {
	i := strings.LastIndex(name, "node_modules/")
	rest := strings.TrimSuffix(name[i+len("node_modules/"):], "/package.json")
	if parts := strings.Split(rest, "/"); len(parts) > 2 || (len(parts) == 2 && !strings.HasPrefix(parts[0], "@")) {
		return ""
	}
	var p struct{ Name, Version string }
	if json.Unmarshal(b, &p) != nil || p.Name == "" || p.Version == "" || p.Name != rest {
		return ""
	}
	return "pkg:npm/" + npmName(p.Name) + "@" + p.Version
}

// npmLock reads a package-lock.json (lockfile versions 2 and 3), its dev dependencies aside.
func npmLock(b []byte) []string {
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
			Dev     bool   `json:"dev"`
		} `json:"packages"`
	}
	if json.Unmarshal(b, &lock) != nil {
		return nil
	}
	var out []string
	for key, p := range lock.Packages {
		i := strings.LastIndex(key, "node_modules/")
		if i < 0 || p.Version == "" || p.Dev {
			continue
		}
		out = append(out, "pkg:npm/"+npmName(key[i+len("node_modules/"):])+"@"+p.Version)
	}
	return out
}

// headers reads "Key: value" lines up to the first empty one.
func headers(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			k = strings.TrimSpace(k)
			if _, set := out[k]; !set {
				out[k] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

// pypiPackage reads a dist-info METADATA or egg-info PKG-INFO.
func pypiPackage(b []byte) string {
	h := headers(b)
	name, version := h["Name"], h["Version"]
	if name == "" || version == "" {
		return ""
	}
	name = strings.ToLower(strings.NewReplacer("_", "-", ".", "-").Replace(name))
	return "pkg:pypi/" + name + "@" + version
}

// mavenPackage reads a jar's META-INF/maven/…/pom.properties.
func mavenPackage(b []byte) string {
	props := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && !strings.HasPrefix(k, "#") {
			props[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	g, a, v := props["groupId"], props["artifactId"], props["version"]
	if g == "" || a == "" || v == "" {
		return ""
	}
	return "pkg:maven/" + g + "/" + a + "@" + v
}

// gemPackage reads a gem's name and version from its specification's file name: rack-3.1.8.gemspec.
func gemPackage(base string) string {
	s := strings.TrimSuffix(base, ".gemspec")
	i := strings.LastIndex(s, "-")
	if i <= 0 || i == len(s)-1 || s[i+1] < '0' || s[i+1] > '9' {
		return ""
	}
	return "pkg:gem/" + s[:i] + "@" + s[i+1:]
}

// nugetPackages reads a .NET deps.json: its libraries of type package.
func nugetPackages(b []byte) []string {
	var deps struct {
		Libraries map[string]struct {
			Type string `json:"type"`
		} `json:"libraries"`
	}
	if json.Unmarshal(b, &deps) != nil {
		return nil
	}
	var out []string
	for key, l := range deps.Libraries {
		name, version, ok := strings.Cut(key, "/")
		if ok && l.Type == "package" {
			out = append(out, "pkg:nuget/"+name+"@"+version)
		}
	}
	return out
}

// goMagic marks the build information in a Go binary.
var goMagic = []byte("\xff Go buildinf:")

// goPackages reads a Go binary's build information: its modules, and the Go standard library it was built with.
func goPackages(b []byte) []string {
	if !bytes.Contains(b, goMagic) {
		return nil
	}
	info, err := buildinfo.Read(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	var out []string
	if v := strings.TrimPrefix(info.GoVersion, "go"); v != "" {
		out = append(out, "pkg:golang/stdlib@"+strings.SplitN(v, " ", 2)[0])
	}
	for _, d := range info.Deps {
		m := d
		if d.Replace != nil {
			m = d.Replace
		}
		if m.Version != "" && m.Version != "(devel)" {
			out = append(out, "pkg:golang/"+m.Path+"@"+m.Version)
		}
	}
	return out
}
