// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"

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
