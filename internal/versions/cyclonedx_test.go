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

func TestInventoryBOM(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	items := []InventoryItem{
		{Environment: "prod", Target: "k8s-a", Platform: "kubernetes", Service: "web", Namespace: "shop", Workload: "web", Image: "nginx:1.27.2", Tag: "1.27.2", Digest: digest},
		{Environment: "prod", Target: "k8s-b", Platform: "kubernetes", Service: "web", Namespace: "shop", Workload: "web", Image: "nginx:1.27.2", Tag: "1.27.2", Digest: digest},
		{Environment: "staging", Target: "vm-1", Platform: "host", Kind: "host_os", Image: "docker.io/library/debian:12.7", Tag: "12.7"},
		{Environment: "staging", Target: "vm-1", Platform: "host", Kind: "host_package", Image: "pkg.goliash/openssl:3.0.15", Tag: "3.0.15"},
	}
	sboms := map[string]store.ImageSBOM{"docker.io/library/nginx@" + digest: {
		Purls:    []string{"pkg:deb/debian/openssl@3.0.15-1~deb12u1?arch=amd64"},
		Licenses: map[string]string{"pkg:deb/debian/openssl@3.0.15-1~deb12u1?arch=amd64": "Apache-2.0"},
	}}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	b := InventoryBOM(items, sboms, "Default", "1.29.0", &at, at)
	raw, _ := json.Marshal(b)
	if b.BOMFormat != "CycloneDX" || b.SpecVersion != "1.6" || !strings.HasPrefix(b.SerialNumber, "urn:uuid:") || len(b.Components) != 3 {
		t.Fatalf("bom %s", raw)
	}
	var nginx, os, pkg BOMComponent
	for _, c := range b.Components {
		switch c.Type {
		case "container":
			nginx = c
		case "operating-system":
			os = c
		case "application":
			pkg = c
		}
	}
	if nginx.Purl != "pkg:oci/nginx@sha256%3A"+strings.Repeat("ab", 32)+"?repository_url=docker.io%2Flibrary%2Fnginx&tag=1.27.2" ||
		len(nginx.Hashes) != 1 || nginx.Hashes[0].Content != strings.Repeat("ab", 32) {
		t.Fatalf("container %+v", nginx)
	}
	targets := 0
	for _, p := range nginx.Properties {
		if p.Name == "goliash:target" {
			targets++
		}
	}
	if targets != 2 || !hasProperty(nginx.Properties, BOMProperty{"goliash:workload", "shop/web"}) {
		t.Fatalf("properties %+v", nginx.Properties)
	}
	if len(nginx.Components) != 1 || nginx.Components[0].Name != "debian/openssl" || nginx.Components[0].Version != "3.0.15-1~deb12u1" {
		t.Fatalf("packages %+v", nginx.Components)
	}
	if l := nginx.Components[0].Licenses; len(l) != 1 || l[0].License == nil || l[0].License.Name != "Apache-2.0" {
		t.Fatalf("license %s", raw)
	}
	if l := bomLicenses("MIT OR Apache-2.0"); len(l) != 1 || l[0].Expression != "MIT OR Apache-2.0" || bomLicenses("") != nil {
		t.Fatalf("expression %+v", l)
	}
	if os.Name != "docker.io/library/debian" || os.Version != "12.7" || pkg.Name != "openssl" || pkg.Purl != "" {
		t.Fatalf("host %+v %+v", os, pkg)
	}
	if b.Metadata.Properties[0].Value != "2026-10-01T12:00:00Z" {
		t.Fatal("point in time missing")
	}
}
