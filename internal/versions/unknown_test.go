// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestWhyUnknown(t *testing.T) {
	two := 2
	base := func() Overview {
		return Overview{
			Upstreams: map[string]Upstream{}, Refs: map[string]Reference{"s": {Repo: "ghcr.io/acme/api", Tag: "1.4.0"}},
			Services: map[string]store.Service{"s": {ID: "s", Name: "api"}}, Policies: map[string]Policy{},
			CheckErrs: map[string]string{}, CheckedAt: map[string]time.Time{"s": time.Now()}, TagCount: map[string]int{"s": 12},
			Registries: RegistryAuth{},
		}
	}
	for _, c := range []struct {
		name   string
		change func(*Overview)
		reason string
		where  string
	}{
		{"no image", func(o *Overview) { o.Refs["s"] = Reference{Tag: "1.4.0"} }, "no image Goliash can look up", "upstream"},
		{"latest", func(o *Overview) { o.Refs["s"] = Reference{Repo: "ghcr.io/acme/api", Tag: "latest"} }, `runs the tag "latest", which is not a version`, ""},
		{"denied", func(o *Overview) { o.CheckErrs["s"] = "denied: requested access to the resource is denied" }, "The last look at ghcr.io/acme/api failed", "registries"},
		{"not found", func(o *Overview) { o.CheckErrs["s"] = "manifest unknown: NAME_UNKNOWN" }, "failed", "upstream"},
		{"private", func(o *Overview) {
			o.Services["s"] = store.Service{ID: "s", PrivateUpstream: "ghcr.io/acme/api"}
			o.TagCount["s"] = 0
		}, "is private: the agents check it", "registries"},
		{"never checked", func(o *Overview) { o.CheckedAt["s"] = time.Time{} }, "has not looked at ghcr.io/acme/api yet", ""},
		{"pinned major", func(o *Overview) {
			o.Upstreams["s"] = Upstream{HasLatestAny: true, LatestAny: Version{Raw: "3.1.0"}}
			o.Policies["s"] = Policy{PinMajor: &two}
		}, "pins major 2, and no release of it is in ghcr.io/acme/api (the newest is 3.1.0)", "policy"},
		{"no tags", func(o *Overview) { o.TagCount["s"] = 0 }, "lists no tags", ""},
		{"nothing comparable", func(*Overview) {}, `None of the 12 tags of ghcr.io/acme/api is comparable with "1.4.0"`, "policy"},
	} {
		o := base()
		c.change(&o)
		u, ok := WhyUnknown(o, "s")
		if !ok || !strings.Contains(u.Reason, c.reason) || u.Where != c.where {
			t.Errorf("%s: %+v %v", c.name, u, ok)
		}
	}
	o := base()
	o.Upstreams["s"] = Upstream{HasLatest: true}
	if _, ok := WhyUnknown(o, "s"); ok {
		t.Error("a known release has a reason to be unknown")
	}
}
