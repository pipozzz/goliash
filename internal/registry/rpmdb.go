// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // the RPM database of RHEL 9, Rocky 9, AlmaLinux 9, Fedora and Amazon Linux 2023
)

// An RPM-based image (Red Hat UBI, Rocky, AlmaLinux, Fedora, Amazon Linux 2023) keeps its packages in a SQLite
// file, rpmdb.sqlite, one row per package holding its RPM header. Older releases (RHEL 8, Amazon Linux 2) keep a
// Berkeley DB instead, which is not read.

// rpmDBPaths are where the SQLite RPM database is: the usual place, and Fedora's newer one.
var rpmDBPaths = map[string]bool{"var/lib/rpm/rpmdb.sqlite": true, "usr/lib/sysimage/rpm/rpmdb.sqlite": true}

// rpmBerkeleyPaths are the Berkeley DB (and NDB) databases of older releases.
var rpmBerkeleyPaths = map[string]bool{"var/lib/rpm/Packages": true, "var/lib/rpm/Packages.db": true, "usr/lib/sysimage/rpm/Packages.db": true}

// errRPMBerkeley means an image's RPM database is a Berkeley DB, which Goliash does not read.
var errRPMBerkeley = errors.New("its RPM database is a Berkeley DB (RHEL 8, Amazon Linux 2 and older), which is not read")

// RPMPackage is a package an RPM database lists: Version is the version and release (3.0.7-27.el9).
type RPMPackage struct{ Name, Version, Epoch, Arch, Source string }

// RPMPackages reads the packages of an rpmdb.sqlite file's bytes (RHEL 9 and later, Fedora, Amazon Linux 2023):
// for a host's database as for an image's.
func RPMPackages(ctx context.Context, b []byte) ([]RPMPackage, error) {
	ps, err := rpmPackages(ctx, b)
	out := make([]RPMPackage, len(ps))
	for i, p := range ps {
		out[i] = RPMPackage{Name: p.name, Version: p.version, Epoch: p.epoch, Arch: p.arch, Source: p.source}
	}
	return out, err
}

// rpmPackages reads the packages of an rpmdb.sqlite file.
func rpmPackages(ctx context.Context, b []byte) ([]osPackage, error) {
	f, err := os.CreateTemp("", "goliash-rpmdb-*.sqlite")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+f.Name()+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, "SELECT blob FROM Packages")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []osPackage
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, err
		}
		if p, ok := rpmHeader(blob); ok {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// RPM header tags and types read.
const (
	rpmTagName      = 1000
	rpmTagVersion   = 1001
	rpmTagRelease   = 1002
	rpmTagEpoch     = 1003
	rpmTagArch      = 1022
	rpmTagSourceRPM = 1044
	rpmTypeInt32    = 4
	rpmTypeString   = 6
	rpmMaxEntries   = 1 << 16
)

// rpmHeader reads a package from an RPM header as the database stores it: the count of index entries and the
// size of the data, the entries (tag, type, offset, count), then the data.
// https://rpm-software-management.github.io/rpm/manual/format_v4.html
func rpmHeader(b []byte) (osPackage, bool) {
	var p osPackage
	if len(b) < 8 {
		return p, false
	}
	il, dl := binary.BigEndian.Uint32(b), binary.BigEndian.Uint32(b[4:])
	if il > rpmMaxEntries || uint64(8)+16*uint64(il)+uint64(dl) > uint64(len(b)) {
		return p, false
	}
	data := b[8+16*il : 8+16*il+dl]
	var version, release string
	for i := range il {
		e := b[8+16*i : 8+16*i+16]
		tag, typ, off := binary.BigEndian.Uint32(e), binary.BigEndian.Uint32(e[4:]), binary.BigEndian.Uint32(e[8:])
		if off >= dl {
			continue
		}
		str := func() string {
			s := data[off:]
			if n := strings.IndexByte(string(s), 0); n >= 0 {
				return string(s[:n])
			}
			return ""
		}
		switch {
		case typ == rpmTypeString && tag == rpmTagName:
			p.name = str()
		case typ == rpmTypeString && tag == rpmTagVersion:
			version = str()
		case typ == rpmTypeString && tag == rpmTagRelease:
			release = str()
		case typ == rpmTypeString && tag == rpmTagArch:
			p.arch = str()
		case typ == rpmTypeString && tag == rpmTagSourceRPM:
			p.source = rpmSourceName(str())
		case typ == rpmTypeInt32 && tag == rpmTagEpoch && off+4 <= dl:
			p.epoch = strconv.FormatUint(uint64(binary.BigEndian.Uint32(data[off:])), 10)
		}
	}
	if p.name == "" || version == "" || p.name == "gpg-pubkey" { // gpg-pubkey: an imported signing key, not software
		return p, false
	}
	p.version = version
	if release != "" {
		p.version += "-" + release
	}
	return p, true
}

// rpmSourceName is the name of a source package from its file name: openssl from openssl-3.0.7-27.el9.src.rpm.
func rpmSourceName(file string) string {
	s := strings.TrimSuffix(strings.TrimSuffix(file, ".src.rpm"), ".nosrc.rpm")
	for range 2 { // the release, then the version
		i := strings.LastIndexByte(s, '-')
		if i <= 0 {
			return ""
		}
		s = s[:i]
	}
	return s
}

// rpmNamespaces are the package URL namespaces of the os-release IDs, as SBOM tools write them.
var rpmNamespaces = map[string]string{"rhel": "redhat"}

func rpmPurl(distro, osVersion string, p osPackage) string {
	if ns := rpmNamespaces[distro]; ns != "" {
		distro = ns
	}
	q := url.Values{}
	if p.arch != "" {
		q.Set("arch", p.arch)
	}
	if p.epoch != "" && p.epoch != "0" {
		q.Set("epoch", p.epoch)
	}
	if osVersion != "" {
		q.Set("os_version", osVersion)
	}
	if p.source != "" && p.source != p.name {
		q.Set("upstream", p.source)
	}
	return "pkg:rpm/" + distro + "/" + p.name + "@" + url.PathEscape(p.version) + "?" + q.Encode()
}
