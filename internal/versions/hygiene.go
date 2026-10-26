// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// Finding is one image hygiene problem: something that makes "what runs" hard to know
// or to trust.
type Finding struct {
	Kind     string // moving-tag, retagged, untrusted-registry, outdated-base, over-budget, unexpected-signer, unpinned
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
		if Managed(ref.Repo()) {
			continue // a managed database or cache: AWS runs and patches it, no image to pin
		}
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
		declared[t.ID] = Declared(t.Platform) || Manual(t.Platform) || Packaged(t.Platform)
	}
	running := active[:0]
	for _, i := range active {
		if !declared[i.TargetID] { // Compose files and people enter tags; digests are not theirs to report
			running = append(running, i)
		}
	}
	findings := Hygiene(running, envName, targetName, allowedRegistries)
	evidence, err := st.ListImageEvidence(ctx, sc)
	if err != nil {
		return nil, err
	}
	services, err := st.ListServices(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := append(OutdatedBases(running, evidence, envName, targetName, time.Now()), OverBudget(running, evidence, services, envName, targetName)...)
	out = append(out, UnexpectedSigners(running, evidence, services, envName, targetName)...)
	out = append(out, findings...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity == "warning" && out[j].Severity != "warning" })
	return out, nil
}

// OutdatedBases finds images built on an older build of their base than the base's tag has now: a build cache
// or a FROM without --pull kept the old base's layers, and its fixed vulnerabilities with them.
func OutdatedBases(active []store.Instance, evidence map[string]store.ImageEvidence, envName, targetName map[string]string, now time.Time) []Finding {
	byImage := map[string]*Finding{}
	var order []string
	for _, i := range active {
		if i.Digest == "" {
			continue
		}
		ref := ParseImage(i.Image)
		ev := evidence[ref.Repo()+"@"+i.Digest]
		if ev.BaseStatus != "outdated" {
			continue
		}
		key := ref.Repo() + ":" + ref.Tag
		f := byImage[key]
		if f == nil {
			stale, rebuild := OutdatedBase(ev)
			base := ShortImage(stale)
			detail := "built on an older " + base + " than the one its tag has now"
			if rebuild != "" {
				detail = "built on " + ShortImage(ev.BaseImage) + ", " + chainVia(ev, rebuild) + "built on an older " + base + " than the one its tag has now"
			}
			if !ev.BaseSince.IsZero() {
				detail += " (built " + ev.BaseSince.Format("Jan 2") + ", " + strconv.Itoa(int(now.Sub(ev.BaseSince).Hours()/24)) + " days ago)"
			}
			fix := ": rebuild with --pull to get its fixes"
			if rebuild != "" {
				fix = ": rebuild " + ShortImage(rebuild) + " with --pull, then this image"
			}
			f = &Finding{
				Kind: "outdated-base", Severity: "warning", Detail: detail + fix,
				Image: strings.TrimPrefix(strings.TrimPrefix(key, "docker.io/library/"), "docker.io/"),
			}
			byImage[key] = f
			order = append(order, key)
		}
		f.Where = append(f.Where, envName[i.EnvironmentID]+" / "+targetName[i.TargetID]+" / "+i.WorkloadName+" / "+i.ContainerName)
	}
	sort.Strings(order)
	out := make([]Finding, 0, len(order))
	for _, k := range order {
		sort.Strings(byImage[k].Where)
		out = append(out, *byImage[k])
	}
	return out
}

// OverBudget finds images bigger than their service's size budget (the version policy's max size).
func OverBudget(active []store.Instance, evidence map[string]store.ImageEvidence, services []store.Service, envName, targetName map[string]string) []Finding {
	budget := map[string]int64{}
	for _, s := range services {
		if b := SizeBudget(s); b > 0 {
			budget[s.ID] = b
		}
	}
	byImage := map[string]*Finding{}
	var order []string
	for _, i := range active {
		b := budget[i.ServiceID]
		if b == 0 || i.Digest == "" {
			continue
		}
		ref := ParseImage(i.Image)
		size := evidence[ref.Repo()+"@"+i.Digest].Size
		if size <= b {
			continue
		}
		key := ref.Repo() + ":" + ref.Tag
		f := byImage[key]
		if f == nil {
			f = &Finding{
				Kind: "over-budget", Severity: "warning",
				Image:  strings.TrimPrefix(strings.TrimPrefix(key, "docker.io/library/"), "docker.io/"),
				Detail: fmt.Sprintf("%d MB, over its %d MB budget by %d MB", size/1_000_000, b/1_000_000, (size-b+999_999)/1_000_000),
			}
			byImage[key] = f
			order = append(order, key)
		}
		f.Where = append(f.Where, envName[i.EnvironmentID]+" / "+targetName[i.TargetID]+" / "+i.WorkloadName+" / "+i.ContainerName)
	}
	sort.Strings(order)
	out := make([]Finding, 0, len(order))
	for _, k := range order {
		sort.Strings(byImage[k].Where)
		out = append(out, *byImage[k])
	}
	return out
}

// UnexpectedSigners finds images of services whose policy names who should sign them, and that are not signed, are
// signed by someone else, or are signed without a certificate naming who (a key-based signature).
func UnexpectedSigners(active []store.Instance, evidence map[string]store.ImageEvidence, services []store.Service, envName, targetName map[string]string) []Finding {
	expected := map[string]string{}
	for _, s := range services {
		if e := ExpectedSigner(s); e != "" {
			expected[s.ID] = e
		}
	}
	byImage := map[string]*Finding{}
	var order []string
	for _, i := range active {
		want := expected[i.ServiceID]
		if want == "" || i.Digest == "" || !i.IsMain {
			continue
		}
		ref := ParseImage(i.Image)
		ev, ok := evidence[ref.Repo()+"@"+i.Digest]
		if !ok || ev.CheckedAt.IsZero() || ev.Error != "" || (ev.Signed && !ev.SignersRead) {
			continue // not looked up (yet), or not who signed it
		}
		detail := ""
		switch {
		case !ev.Signed:
			detail = "not signed; " + want + " should sign it"
		case len(ev.Signers) == 0:
			detail = "signed without a certificate naming who (a key); " + want + " should sign it"
		default:
			match := false
			for _, s := range ev.Signers {
				match = match || registry.SignerMatches(s.Identity, want)
			}
			if match {
				continue
			}
			detail = "signed by " + ev.Signers[0].Identity + "; " + want + " should sign it"
		}
		key := ref.Repo() + "@" + i.Digest
		f := byImage[key]
		if f == nil {
			f = &Finding{
				Kind: "unexpected-signer", Severity: "warning",
				Image:  strings.TrimPrefix(strings.TrimPrefix(ref.Repo()+":"+ref.Tag, "docker.io/library/"), "docker.io/"),
				Detail: detail,
			}
			byImage[key] = f
			order = append(order, key)
		}
		f.Where = append(f.Where, envName[i.EnvironmentID]+" / "+targetName[i.TargetID]+" / "+i.WorkloadName+" / "+i.ContainerName)
	}
	sort.Strings(order)
	out := make([]Finding, 0, len(order))
	for _, k := range order {
		sort.Strings(byImage[k].Where)
		out = append(out, *byImage[k])
	}
	return out
}
