// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
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
