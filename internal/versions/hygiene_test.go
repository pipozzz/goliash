// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestHygiene(t *testing.T) {
	inst := func(target, image, digest string) store.Instance {
		return store.Instance{TargetID: target, EnvironmentID: "prod", WorkloadName: "w", ContainerName: "c", Image: image, Digest: digest}
	}
	active := []store.Instance{
		inst("a", "nginx:latest", "sha256:1"),
		inst("a", "ghcr.io/acme/api:1.4", "sha256:2"),
		inst("b", "ghcr.io/acme/api:1.4", "sha256:3"),
		inst("a", "quay.io/acme/worker:2.0", ""),
		inst("a", "redis", "sha256:4"),
	}
	envs, targets := map[string]string{"prod": "prod"}, map[string]string{"a": "k8s-a", "b": "k8s-b"}
	got := map[string]Finding{}
	for _, f := range Hygiene(active, envs, targets, []string{"ghcr.io/acme", "docker.io"}) {
		got[f.Kind+" "+f.Image] = f
	}
	for _, want := range []string{
		"moving-tag nginx:latest", "moving-tag redis:latest", "retagged ghcr.io/acme/api:1.4",
		"untrusted-registry quay.io/acme/worker:2.0", "unpinned quay.io/acme/worker:2.0",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if f := got["retagged ghcr.io/acme/api:1.4"]; len(f.Where) != 2 || f.Severity != "warning" {
		t.Fatalf("retagged %+v", f)
	}
	if _, ok := got["untrusted-registry ghcr.io/acme/api:1.4"]; ok {
		t.Fatal("ghcr.io/acme is allowed")
	}
	if n := len(Hygiene(active, envs, targets, nil)); n != len(got)-1 {
		t.Fatalf("without an allowed list there is no registry finding: %d", n)
	}
}

func TestUnexpectedSigners(t *testing.T) {
	now := time.Now()
	svc := store.Service{ID: "s1", Name: "api", VersionPolicy: json.RawMessage(`{"signer":"https://github.com/acme/*"}`)}
	other := store.Service{ID: "s2", Name: "web"} // no expectation
	run := func(service, image, digest string) store.Instance {
		return store.Instance{ServiceID: service, EnvironmentID: "e", TargetID: "t", WorkloadName: "w", ContainerName: "c", Image: image, Digest: digest, IsMain: true}
	}
	active := []store.Instance{
		run("s1", "ghcr.io/acme/api:1.0.0", "sha256:good"),
		run("s1", "ghcr.io/acme/api:1.1.0", "sha256:evil"),
		run("s1", "ghcr.io/acme/api:1.2.0", "sha256:unsigned"),
		run("s1", "ghcr.io/acme/api:1.3.0", "sha256:key"),
		run("s1", "ghcr.io/acme/api:1.4.0", "sha256:old"), // looked up before signers were read
		run("s2", "nginx:1.27", "sha256:web"),
	}
	gh := store.ImageSigner{Identity: "https://github.com/acme/api/.github/workflows/release.yml@refs/heads/main", Issuer: "https://token.actions.githubusercontent.com"}
	evidence := map[string]store.ImageEvidence{
		"ghcr.io/acme/api@sha256:good":       {CheckedAt: now, Signed: true, SignersRead: true, Signers: []store.ImageSigner{gh}},
		"ghcr.io/acme/api@sha256:evil":       {CheckedAt: now, Signed: true, SignersRead: true, Signers: []store.ImageSigner{{Identity: "mallory@example.com"}}},
		"ghcr.io/acme/api@sha256:unsigned":   {CheckedAt: now, SignersRead: true},
		"ghcr.io/acme/api@sha256:key":        {CheckedAt: now, Signed: true, SignersRead: true},
		"ghcr.io/acme/api@sha256:old":        {CheckedAt: now, Signed: true},
		"docker.io/library/nginx@sha256:web": {CheckedAt: now},
	}
	got := UnexpectedSigners(active, evidence, []store.Service{svc, other}, map[string]string{"e": "prod"}, map[string]string{"t": "k8s"})
	if len(got) != 3 {
		t.Fatalf("findings %+v", got)
	}
	details := got[0].Detail + " | " + got[1].Detail + " | " + got[2].Detail
	for _, want := range []string{"signed by mallory@example.com", "not signed", "without a certificate naming who"} {
		if !strings.Contains(details, want) {
			t.Errorf("missing %q in %s", want, details)
		}
	}
	if got[0].Kind != "unexpected-signer" || got[0].Where[0] != "prod / k8s / w / c" {
		t.Errorf("finding %+v", got[0])
	}
}

func TestQuietUpstreams(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	active := []store.Instance{
		{ServiceID: "s1", Image: "docker.io/acme/legacy:1.2.0", EnvironmentID: "e", TargetID: "t", WorkloadName: "legacy", ContainerName: "app"},
		{ServiceID: "s2", Image: "docker.io/library/nginx:1.27.2", EnvironmentID: "e", TargetID: "t", WorkloadName: "web", ContainerName: "nginx"},
		{ServiceID: "s3", Image: "123.dkr.ecr.eu-west-1.amazonaws.com/billing:4.1", EnvironmentID: "e", TargetID: "t", WorkloadName: "billing", ContainerName: "app"},
		{ServiceID: "s4", Image: "docker.io/acme/broken:1.0", EnvironmentID: "e", TargetID: "t", WorkloadName: "broken", ContainerName: "app"},
	}
	old := now.AddDate(-2, 0, 0)
	releases := map[string][]store.Release{
		"s1": {{Version: "1.2.0", DiscoveredAt: old, PublishedAt: now.AddDate(0, -14, 0)}},
		"s2": {{Version: "1.27.2", DiscoveredAt: old}, {Version: "1.27.3", DiscoveredAt: now.AddDate(0, -1, 0)}},
		"s3": {{Version: "4.1", DiscoveredAt: now.AddDate(0, -13, 0)}},
		"s4": {{Version: "1.0", DiscoveredAt: old}},
	}
	checks := map[string]store.UpstreamCheck{
		"s1": {At: now.Add(-time.Hour)}, "s2": {At: now.Add(-time.Hour)}, "s3": {At: now.Add(-time.Hour)},
		"s4": {At: now.Add(-time.Hour), Error: "unauthorized"},
	}
	services := []store.Service{{ID: "s1", Name: "legacy"}, {ID: "s2", Name: "nginx"}, {ID: "s3", Name: "billing"}, {ID: "s4", Name: "broken"}}
	got := QuietUpstreams(active, releases, checks, services, map[string]string{"e": "prod"}, map[string]string{"t": "eks"}, now)
	if len(got) != 2 {
		t.Fatalf("findings %+v", got)
	}
	if got[0].Image != "acme/legacy" || got[0].Severity != "warning" || !strings.Contains(got[0].Detail, "since Sep 2025 (14 months)") {
		t.Fatalf("public upstream %+v", got[0])
	}
	if got[1].Severity != "info" || !strings.Contains(got[1].Detail, "billing: no new image pushed") {
		t.Fatalf("in-house image %+v", got[1])
	}
	if got[0].Where[0] != "prod / eks / legacy / app" {
		t.Fatalf("where %v", got[0].Where)
	}
}
