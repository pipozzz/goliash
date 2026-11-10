// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

func collect(t *testing.T, root string, settings agentproto.HostSettings) map[string]string {
	t.Helper()
	settings.Root = &root
	c, err := New(context.Background(), agentproto.Target{Host: &settings})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, w := range res.Workloads {
		out[w.ID] = string(w.Kind) + " " + w.Name + " " + w.Containers[0].Image
	}
	if !res.Complete {
		t.Errorf("incomplete: %v", res.Errors)
	}
	return out
}

func TestUbuntu(t *testing.T) {
	got := collect(t, "testdata/ubuntu", agentproto.HostSettings{Packages: []string{"haproxy"}})
	want := map[string]string{
		"os/ubuntu":                   "host_os ubuntu docker.io/library/ubuntu:22.04",
		"pkg/nginx-core":              "host_package nginx docker.io/library/nginx:1.18.0",
		"pkg/postgresql-14":           "host_package postgresql docker.io/library/postgres:14.13",
		"pkg/openssl":                 "host_package openssl pkg.goliash/openssl:3.0.2",
		"pkg/openjdk-17-jre-headless": "host_package openjdk docker.io/library/eclipse-temurin:17.0.12",
		"pkg/haproxy":                 "host_package haproxy docker.io/library/haproxy:2.4.24",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %q, want %q", id, got[id], w)
		}
	}
	if _, ok := got["pkg/redis-server"]; ok {
		t.Error("a removed package (config files only) reported")
	}
	if _, ok := got["pkg/libc6"]; ok {
		t.Error("an unlisted package reported")
	}
	if all := collect(t, "testdata/ubuntu", agentproto.HostSettings{AllPackages: ptr(true)}); !strings.HasPrefix(all["pkg/libc6"], "host_package libc6 pkg.goliash/libc6:2.35") {
		t.Errorf("all packages: %q", all["pkg/libc6"])
	}
}

func TestAlpine(t *testing.T) {
	got := collect(t, "testdata/alpine", agentproto.HostSettings{})
	if got["os/alpine"] != "host_os alpine docker.io/library/alpine:3.20.3" || got["pkg/nginx"] != "host_package nginx docker.io/library/nginx:1.26.2" ||
		got["pkg/libssl3"] != "host_package openssl pkg.goliash/openssl:3.3.2" || got["pkg/musl"] != "" {
		t.Errorf("alpine: %v", got)
	}
}

// Rocky 9 keeps its packages in RPM's SQLite database (testdata/rocky/var/lib/rpm/rpmdb.sqlite holds nginx,
// openssl-libs and postgresql-server headers).
func TestRocky(t *testing.T) {
	got := collect(t, "testdata/rocky", agentproto.HostSettings{})
	if got["os/rocky"] != "host_os rocky docker.io/library/rockylinux:9.3" || got["pkg/nginx"] != "host_package nginx docker.io/library/nginx:1.20.1" ||
		got["pkg/openssl-libs"] != "host_package openssl pkg.goliash/openssl:3.0.7" || got["pkg/postgresql-server"] != "host_package postgresql docker.io/library/postgres:13.14" {
		t.Errorf("rocky: %v", got)
	}
}

func TestNoHost(t *testing.T) {
	root := t.TempDir()
	if _, err := New(context.Background(), agentproto.Target{Host: &agentproto.HostSettings{Root: &root}}); err == nil {
		t.Error("a root without os-release accepted")
	}
}

func TestUpstreamVersion(t *testing.T) {
	for in, want := range map[string]string{
		"1:1.18.0-6ubuntu14.4": "1.18.0", "15.8-0+deb12u1": "15.8", "3.3.2-r0": "3.3.2", "17.0.12+7-1ubuntu2~22.04": "17.0.12", "~rc": "",
	} {
		if got := upstreamVersion(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// A newer kernel installed than the one running, or Debian's reboot-required, is a reboot waiting.
func TestRebootRequired(t *testing.T) {
	write := func(root, name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dpkg := func(names ...string) string {
		var b strings.Builder
		for _, n := range names {
			b.WriteString("Package: " + n + "\nStatus: install ok installed\nVersion: 1\n\n")
		}
		return b.String()
	}
	maintenance := func(root, running string) []string {
		t.Helper()
		write(root, "etc/os-release", "ID=ubuntu\nVERSION_ID=\"24.04\"\n")
		c, err := New(context.Background(), agentproto.Target{Host: &agentproto.HostSettings{Root: &root}})
		if err != nil {
			t.Fatal(err)
		}
		hc := c.(*Collector)
		hc.osrelease = filepath.Join(root, "osrelease")
		write(root, "osrelease", running+"\n")
		res, err := hc.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range res.Workloads {
			if w.Kind == agentproto.HostOs {
				return w.Maintenance
			}
		}
		t.Fatal("no OS workload")
		return nil
	}

	newer := t.TempDir()
	write(newer, "var/lib/dpkg/status", dpkg("linux-image-6.8.0-1012-aws", "linux-image-6.8.0-1015-aws", "linux-image-aws"))
	if got := maintenance(newer, "6.8.0-1012-aws"); len(got) != 1 || got[0] != "reboot required: kernel 6.8.0-1015-aws installed, 6.8.0-1012-aws runs" {
		t.Errorf("newer kernel: %v", got)
	}
	if got := maintenance(newer, "6.8.0-1015-aws"); len(got) != 0 {
		t.Errorf("running the newest: %v", got)
	}

	flagged := t.TempDir()
	write(flagged, "var/lib/dpkg/status", dpkg("libc6"))
	write(flagged, "var/run/reboot-required", "*** System restart required ***\n")
	write(flagged, "var/run/reboot-required.pkgs", "libc6\nlinux-base\nlibc6\n")
	if got := maintenance(flagged, "6.8.0-1015-aws"); len(got) != 1 || got[0] != "reboot required: libc6, linux-base updated" {
		t.Errorf("reboot-required: %v", got)
	}

	updates := t.TempDir()
	write(updates, "var/lib/dpkg/status", dpkg("libc6"))
	write(updates, "var/lib/update-notifier/updates-available", "\n12 updates can be applied immediately.\n5 of these updates are standard security updates.\nTo see these additional updates run: apt list --upgradable\n")
	if got := maintenance(updates, "6.8.0-1015-aws"); len(got) != 1 || got[0] != "package updates: 12 waiting, 5 of them security" {
		t.Errorf("updates available: %v", got)
	}
	write(updates, "var/lib/update-notifier/updates-available", "\n0 updates can be applied immediately.\n")
	if got := maintenance(updates, "6.8.0-1015-aws"); len(got) != 0 {
		t.Errorf("none waiting: %v", got)
	}

	if compareRelease("5.14.0-503.11.1.el9_5", "5.14.0-427.13.1.el9_4.x86_64") <= 0 || compareRelease("5.14.0-427.13.1.el9_4", "5.14.0-427.13.1.el9_4.x86_64") >= 0 {
		t.Error("RPM kernel releases out of order")
	}
}
