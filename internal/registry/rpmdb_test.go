// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rpmHeaderBlob builds an RPM header as rpmdb.sqlite stores it; epoch < 0 leaves it out.
func rpmHeaderBlob(name, version, release, arch, sourceRPM string, epoch int) []byte {
	type entry struct {
		tag, typ uint32
		data     []byte
	}
	str := func(s string) []byte { return append([]byte(s), 0) }
	es := []entry{{rpmTagName, rpmTypeString, str(name)}, {rpmTagVersion, rpmTypeString, str(version)}, {rpmTagRelease, rpmTypeString, str(release)}}
	if epoch >= 0 {
		es = append(es, entry{rpmTagEpoch, rpmTypeInt32, binary.BigEndian.AppendUint32(nil, uint32(epoch))}) //nolint:gosec // a small test value
	}
	es = append(es, entry{rpmTagArch, rpmTypeString, str(arch)}, entry{rpmTagSourceRPM, rpmTypeString, str(sourceRPM)})
	var index, data []byte
	for _, e := range es {
		for len(data)%4 != 0 {
			data = append(data, 0)
		}
		index = binary.BigEndian.AppendUint32(index, e.tag)
		index = binary.BigEndian.AppendUint32(index, e.typ)
		index = binary.BigEndian.AppendUint32(index, uint32(len(data))) //nolint:gosec // small
		index = binary.BigEndian.AppendUint32(index, 1)
		data = append(data, e.data...)
	}
	out := binary.BigEndian.AppendUint32(nil, uint32(len(es)))  //nolint:gosec // small
	out = binary.BigEndian.AppendUint32(out, uint32(len(data))) //nolint:gosec // small
	return append(append(out, index...), data...)
}

// rpmDB builds an rpmdb.sqlite holding the headers.
func rpmDB(t *testing.T, headers ...[]byte) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rpmdb.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE Packages (hnum INTEGER PRIMARY KEY AUTOINCREMENT, blob BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	for _, h := range headers {
		if _, err := db.ExecContext(context.Background(), "INSERT INTO Packages (blob) VALUES (?)", h); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	b, err := os.ReadFile(path) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRPMHeader(t *testing.T) {
	p, ok := rpmHeader(rpmHeaderBlob("openssl-libs", "3.0.7", "24.el9", "x86_64", "openssl-3.0.7-24.el9.src.rpm", 1))
	if !ok || p != (osPackage{name: "openssl-libs", version: "3.0.7-24.el9", arch: "x86_64", source: "openssl", epoch: "1"}) {
		t.Fatalf("%+v %v", p, ok)
	}
	if _, ok := rpmHeader(rpmHeaderBlob("gpg-pubkey", "fd431d51", "4ae0493b", "", "", -1)); ok {
		t.Error("a signing key read as a package")
	}
	for _, b := range [][]byte{nil, {0, 0, 0, 9, 0, 0, 0, 1}, bytes.Repeat([]byte{0xff}, 40)} {
		if _, ok := rpmHeader(b); ok {
			t.Errorf("garbage %x read", b)
		}
	}
	if got := rpmSourceName("python3.9-3.9.18-1.el9.src.rpm"); got != "python3.9" {
		t.Errorf("source %q", got)
	}
}

func TestImagePackagesRocky(t *testing.T) {
	db := rpmDB(t,
		rpmHeaderBlob("openssl-libs", "3.0.7", "24.el9", "x86_64", "openssl-3.0.7-24.el9.src.rpm", 1),
		rpmHeaderBlob("bash", "5.1.8", "9.el9", "x86_64", "bash-5.1.8-9.el9.src.rpm", -1),
		rpmHeaderBlob("gpg-pubkey", "350d275d", "6279464b", "", "", -1),
	)
	srv := packagesServer(t,
		layer(t, map[string]string{"etc/os-release": "ID=\"rocky\"\nVERSION_ID=\"9.3\"\n", "var/lib/rpm/rpmdb.sqlite": string(db)}),
	)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	sbom, err := client().ImagePackages(context.Background(), host+"/acme/app", "sha256:img", Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	want := "pkg:rpm/rocky/bash@5.1.8-9.el9?arch=x86_64&os_version=9.3 " +
		"pkg:rpm/rocky/openssl-libs@3.0.7-24.el9?arch=x86_64&epoch=1&os_version=9.3&upstream=openssl"
	if got := strings.Join(sbom.Purls, " "); got != want {
		t.Errorf("purls:\n%s\nwant\n%s", got, want)
	}
}

// RHEL is written as SBOM tools write it (redhat); an older Berkeley DB says so.
func TestImagePackagesRHELAndBerkeley(t *testing.T) {
	db := rpmDB(t, rpmHeaderBlob("curl-minimal", "7.76.1", "26.el9", "x86_64", "curl-7.76.1-26.el9.src.rpm", -1))
	srv := packagesServer(t, layer(t, map[string]string{"etc/os-release": "ID=\"rhel\"\nVERSION_ID=\"9.3\"\n", "var/lib/rpm/rpmdb.sqlite": string(db)}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	sbom, err := client().ImagePackages(context.Background(), host+"/acme/app", "sha256:img", Credentials{})
	if err != nil || strings.Join(sbom.Purls, " ") != "pkg:rpm/redhat/curl-minimal@7.76.1-26.el9?arch=x86_64&os_version=9.3&upstream=curl" {
		t.Fatalf("%v %v", sbom.Purls, err)
	}
	old := packagesServer(t, layer(t, map[string]string{"etc/os-release": "ID=\"rocky\"\nVERSION_ID=\"8.9\"\n", "var/lib/rpm/Packages": "\x00\x06\x15\x61"}))
	defer old.Close()
	_, err = client().ImagePackages(context.Background(), strings.TrimPrefix(old.URL, "http://")+"/acme/app", "sha256:img", Credentials{})
	if !errors.Is(err, ErrNoPackageDatabase) || !strings.Contains(err.Error(), "Berkeley DB") {
		t.Errorf("Berkeley DB: %v", err)
	}
}
