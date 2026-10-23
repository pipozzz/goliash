// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
)

// Without an SBOM, an image's operating system packages are still written in it: Debian and Ubuntu keep them in
// /var/lib/dpkg/status (distroless images one file each under /var/lib/dpkg/status.d), Alpine, Wolfi and
// Chainguard in /lib/apk/db/installed. ImagePackages reads those files from the image's layers, newest first, and
// lists the packages as package URLs, as an SBOM would. It does not see what was built from source or copied in,
// nor the application's own dependencies: those take a real SBOM.

// Bounds on reading an image for its packages.
const (
	maxPackagesRead = 1 << 30  // compressed bytes read across an image's layers
	maxPackageFile  = 64 << 20 // one package database or os-release file
)

// ErrNoPackageDatabase means an image has no package database Goliash reads (dpkg, apk): scratch, distroless
// static, RPM-based, or software built from source.
var ErrNoPackageDatabase = errors.New("no dpkg or apk package database in the image")

// ImagePackages lists the operating system packages installed in digest of repository, for linux/amd64 (or the
// first platform), as package URLs: pkg:deb/ubuntu/nginx@1.18.0-6ubuntu14.4?arch=amd64&os_version=22.04.
func (c *Client) ImagePackages(ctx context.Context, repository, digest string, creds Credentials) (SBOM, error) {
	host, repo, err := splitRepository(repository)
	if err != nil {
		return SBOM{}, err
	}
	base := fmt.Sprintf("%s://%s/v2/%s", c.Scheme, host, repo)
	accept := strings.Join([]string{mediaIndex, mediaDockerList, mediaManifest, mediaDockerImage}, ", ")
	get := func(ref string) (evidenceManifest, error) {
		var m evidenceManifest
		body, _, err := c.get(ctx, base+"/manifests/"+ref, repo, creds, accept)
		if err != nil {
			return m, err
		}
		return m, json.Unmarshal(body, &m)
	}
	m, err := get(digest)
	if err != nil {
		return SBOM{}, err
	}
	if len(m.Manifests) > 0 {
		d, ok := platformManifest(m.Manifests)
		if !ok {
			return SBOM{}, fmt.Errorf("%s@%s has no image for a known platform", repository, digest)
		}
		if m, err = get(d.Digest); err != nil {
			return SBOM{}, err
		}
	}
	var fs imageFiles
	read := int64(0)
	for i := len(m.Layers) - 1; i >= 0 && !fs.done(); i-- { // newest first: an upper layer's file wins
		l := m.Layers[i]
		if read += l.Size; read > maxPackagesRead {
			return SBOM{}, fmt.Errorf("the image is larger than %d MB to read for its packages", maxPackagesRead>>20)
		}
		if err := c.readLayer(ctx, base+"/blobs/"+l.Digest, repo, creds, &fs); err != nil {
			return SBOM{}, fmt.Errorf("layer %s: %w", l.Digest, err)
		}
	}
	purls := fs.purls()
	if len(purls) == 0 {
		return SBOM{}, ErrNoPackageDatabase
	}
	return SBOM{Purls: purls}, nil
}

// imageFiles are the files ImagePackages looks for, as the newest layer having each holds them.
type imageFiles struct {
	osRelease []byte
	dpkg      []byte
	dpkgD     map[string][]byte // /var/lib/dpkg/status.d/<package>
	apk       []byte
	seen      map[string]bool // paths already taken from a newer layer, or deleted in one
}

func (f *imageFiles) done() bool {
	return f.osRelease != nil && (f.dpkg != nil || f.apk != nil)
}

// readLayer reads one layer (a gzip or plain tar) for the files not found in a newer one.
func (c *Client) readLayer(ctx context.Context, url, repo string, creds Credentials, fs *imageFiles) error {
	resp, err := c.request(ctx, http.MethodGet, url, repo, creds, "*/*")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry answered %s", resp.Status)
	}
	br := bufio.NewReader(resp.Body)
	var r io.Reader = br
	if magic, _ := br.Peek(4); len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		r = gz
	} else if len(magic) == 4 && bytes.Equal(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		return errors.New("zstd layers are not read")
	}
	if fs.seen == nil {
		fs.seen, fs.dpkgD = map[string]bool{}, map[string][]byte{}
	}
	tr := tar.NewReader(r)
	var deleted []string // whiteouts in this layer hide the lower layers' files, not this one's
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(path.Clean("/"+h.Name), "/")
		if dir, file := path.Split(name); strings.HasPrefix(file, ".wh.") {
			deleted = append(deleted, dir+strings.TrimPrefix(file, ".wh."))
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size > maxPackageFile || fs.seen[name] {
			continue
		}
		var dst *[]byte
		switch {
		case name == "etc/os-release" || name == "usr/lib/os-release":
			if fs.osRelease != nil {
				continue
			}
			dst = &fs.osRelease
		case name == "var/lib/dpkg/status":
			dst = &fs.dpkg
		case strings.HasPrefix(name, "var/lib/dpkg/status.d/") && !strings.HasSuffix(name, ".md5sums"):
			b, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			fs.dpkgD[name] = b
			fs.seen[name] = true
			continue
		case name == "lib/apk/db/installed":
			dst = &fs.apk
		default:
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		*dst = b
		fs.seen[name] = true
	}
	for _, d := range deleted {
		fs.seen[d] = true
	}
	return ctx.Err()
}

// purls lists the packages the files name.
func (f imageFiles) purls() []string {
	id, version := osRelease(f.osRelease)
	set := map[string]bool{}
	if f.dpkg != nil || len(f.dpkgD) > 0 {
		stanzas := [][]byte{f.dpkg}
		names := make([]string, 0, len(f.dpkgD))
		for n := range f.dpkgD {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			stanzas = append(stanzas, f.dpkgD[n])
		}
		if id == "" {
			id = "debian"
		}
		if id == "debian" {
			version, _, _ = strings.Cut(version, ".")
		}
		for _, s := range stanzas {
			for _, p := range dpkgPackages(s) {
				set[debPurl(id, version, p)] = true
			}
		}
	}
	if f.apk != nil {
		if id == "" {
			id = "alpine"
		}
		for _, p := range apkPackages(f.apk) {
			set[apkPurl(id, version, p)] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// osPackage is an installed package as a package database records it.
type osPackage struct{ name, version, arch, source string }

// dpkgPackages reads a dpkg status file: the installed packages of its stanzas.
func dpkgPackages(b []byte) []osPackage {
	var out []osPackage
	for _, stanza := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n\n") {
		var p osPackage
		installed := true
		for _, line := range strings.Split(stanza, "\n") {
			k, v, ok := strings.Cut(line, ": ")
			if !ok || strings.HasPrefix(line, " ") {
				continue
			}
			switch k {
			case "Package":
				p.name = v
			case "Version":
				p.version = v
			case "Architecture":
				p.arch = v
			case "Source":
				p.source, _, _ = strings.Cut(v, " ")
			case "Status":
				installed = strings.HasSuffix(v, " installed")
			}
		}
		if p.name != "" && p.version != "" && installed {
			out = append(out, p)
		}
	}
	return out
}

// apkPackages reads an apk installed database: P: name, V: version, A: arch, o: origin, a blank line between.
func apkPackages(b []byte) []osPackage {
	var out []osPackage
	var p osPackage
	flush := func() {
		if p.name != "" && p.version != "" {
			out = append(out, p)
		}
		p = osPackage{}
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			flush()
			continue
		}
		if len(line) < 2 || line[1] != ':' {
			continue
		}
		switch v := line[2:]; line[0] {
		case 'P':
			p.name = v
		case 'V':
			p.version = v
		case 'A':
			p.arch = v
		case 'o':
			p.source = v
		}
	}
	flush()
	return out
}

// osRelease reads ID and VERSION_ID from an os-release file.
func osRelease(b []byte) (id, version string) {
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			id = strings.ToLower(v)
		case "VERSION_ID":
			version = v
		}
	}
	return id, version
}

func debPurl(distro, osVersion string, p osPackage) string {
	q := url.Values{}
	if p.arch != "" {
		q.Set("arch", p.arch)
	}
	if osVersion != "" {
		q.Set("os_version", osVersion)
	}
	if p.source != "" && p.source != p.name {
		q.Set("upstream", p.source)
	}
	return "pkg:deb/" + distro + "/" + p.name + "@" + url.PathEscape(p.version) + "?" + q.Encode()
}

func apkPurl(distro, osVersion string, p osPackage) string {
	q := url.Values{}
	if p.arch != "" {
		q.Set("arch", p.arch)
	}
	if osVersion != "" {
		q.Set("os_version", osVersion)
	}
	if p.source != "" && p.source != p.name {
		q.Set("origin", p.source)
	}
	return "pkg:apk/" + distro + "/" + p.name + "@" + url.PathEscape(p.version) + "?" + q.Encode()
}
