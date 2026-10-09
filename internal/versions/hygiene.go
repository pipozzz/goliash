// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/pipozzz/goliash/internal/store"
)

// Finding is one image hygiene problem: something that makes "what runs" hard to know
// or to trust.
type Finding struct {
	Kind     string // moving-tag, retagged, untrusted-registry, unpinned
	Severity string // warning or info
	Image    string // repository:tag
	Detail   string
	Where    []string // "env / target / workload / container"
}

// movingTags are tags that point at different images over time.
var movingTags = map[string]bool{
	"": true, "latest": true, "stable": true, "main": true, "master": true, "mainline": true, "edge": true,
	"nightly": true, "dev": true, "develop": true, "next": true, "beta": true, "alpine": true, "slim": true,
}

// Hygiene checks the running containers, sidecars included:
//   - moving-tag: a tag like latest or stable, so the version cannot be known;
//   - retagged: the same repository and tag running as different images (digests);
//   - untrusted-registry: an image from a registry outside allowed (when set);
//   - unpinned: no digest is known for the image (info).
func Hygiene(active []store.Instance, envName, targetName map[string]string, allowed []string) []Finding {
	type group struct {
		where   []string
		digests map[string]bool
	}
	byImage := map[string]*group{}
	var order []string
	for _, i := range active {
		ref := ParseImage(i.Image)
		key := ref.Repo() + ":" + ref.Tag
		if byImage[key] == nil {
			byImage[key] = &group{digests: map[string]bool{}}
			order = append(order, key)
		}
		g := byImage[key]
		g.where = append(g.where, envName[i.EnvironmentID]+" / "+targetName[i.TargetID]+" / "+i.WorkloadName+" / "+i.ContainerName)
		if i.Digest != "" {
			g.digests[i.Digest] = true
		}
	}
	sort.Strings(order)
	var out []Finding
	for _, key := range order {
		g := byImage[key]
		repo, tag, _ := strings.Cut(key, ":")
		sort.Strings(g.where)
		image := strings.TrimPrefix(strings.TrimPrefix(key, "docker.io/library/"), "docker.io/")
		if movingTags[strings.ToLower(tag)] {
			t := tag
			if t == "" {
				t = "no tag"
			}
			out = append(out, Finding{
				Kind: "moving-tag", Severity: "warning", Image: image, Where: g.where,
				Detail: "\"" + t + "\" points at different images over time; pin a version",
			})
		}
		if len(g.digests) > 1 {
			out = append(out, Finding{
				Kind: "retagged", Severity: "warning", Image: image, Where: g.where,
				Detail: "the same tag runs as " + strconv.Itoa(len(g.digests)) + " different images; it was pushed again",
			})
		}
		if len(allowed) > 0 && !allowedRegistry(repo, allowed) {
			out = append(out, Finding{
				Kind: "untrusted-registry", Severity: "warning", Image: image, Where: g.where,
				Detail: "registry is not in the allowed list",
			})
		}
		if len(g.digests) == 0 {
			out = append(out, Finding{
				Kind: "unpinned", Severity: "info", Image: image, Where: g.where,
				Detail: "no digest reported, so a tag pushed again would go unnoticed",
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity == "warning" && out[j].Severity != "warning" })
	return out
}

// allowedRegistry reports whether repo ("ghcr.io/acme/app") is under one of the allowed
// prefixes ("ghcr.io/acme", "docker.io").
func allowedRegistry(repo string, allowed []string) bool {
	for _, a := range allowed {
		a = strings.TrimSuffix(strings.TrimSpace(a), "/")
		if a != "" && (repo == a || strings.HasPrefix(repo, a+"/")) {
			return true
		}
	}
	return false
}

// allowedRegistries is the server's GOLIASH_ALLOWED_REGISTRIES; empty means any.
var allowedRegistries []string

// SetAllowedRegistries sets the registries (or registry/namespace prefixes) images may
// come from; empty turns the check off.
func SetAllowedRegistries(prefixes []string) { allowedRegistries = prefixes }

// LoadHygiene checks the running containers of a workspace.
func LoadHygiene(ctx context.Context, st *store.Store, sc store.Scope) ([]Finding, error) {
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return nil, err
	}
	envs, err := st.ListEnvironments(ctx, sc)
	if err != nil {
		return nil, err
	}
	targets, err := st.ListTargets(ctx, sc)
	if err != nil {
		return nil, err
	}
	envName, targetName := map[string]string{}, map[string]string{}
	declared := map[string]bool{}
	for _, e := range envs {
		envName[e.ID] = e.Name
	}
	for _, t := range targets {
		targetName[t.ID] = t.Name
		declared[t.ID] = Declared(t.Platform)
	}
	running := active[:0]
	for _, i := range active {
		if !declared[i.TargetID] { // Compose files declare tags; digests are not theirs to report
			running = append(running, i)
		}
	}
	return Hygiene(running, envName, targetName, allowedRegistries), nil
}
