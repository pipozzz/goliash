// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"fmt"
	"slices"
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
	Kind     string // moving-tag, retagged, untrusted-registry, outdated-base, over-budget, unexpected-signer, quiet-upstream, eol-os, eol-runtime, unpinned
	Severity string // warning or info
	Image    string // repository:tag
	Detail   string
	Where    []string // "env / target / workload / container"
	Teams    []string // the owners of the services running it
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
	releases, err := st.ReleasesByService(ctx, sc)
	if err != nil {
		return nil, err
	}
	checks, err := st.UpstreamChecks(ctx, sc)
	if err != nil {
		return nil, err
	}
	out = append(out, QuietUpstreams(running, releases, checks, services, envName, targetName, time.Now())...)
	sboms, err := st.ListImageSBOMs(ctx, sc)
	if err != nil {
		return nil, err
	}
	out = append(out, EOLOperatingSystems(ctx, running, sboms, envName, targetName, time.Now())...)
	out = append(out, EOLRuntimes(ctx, running, sboms, envName, targetName, time.Now())...)
	out = append(out, findings...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity == "warning" && out[j].Severity != "warning" })
	withTeams(out, running, services)
	return out, nil
}

// withTeams names, on each finding, the owners of the services running its image: by repository and tag, else (a
// quiet upstream names the repository alone) by repository.
func withTeams(findings []Finding, active []store.Instance, services []store.Service) {
	owner := map[string]string{}
	for _, s := range services {
		owner[s.ID] = s.Owner
	}
	byImage, byRepo := map[string][]string{}, map[string][]string{}
	short := func(s string) string {
		return strings.TrimPrefix(strings.TrimPrefix(s, "docker.io/library/"), "docker.io/")
	}
	for _, i := range active {
		o := owner[i.ServiceID]
		if o == "" {
			continue
		}
		ref := ParseImage(i.Image)
		k, r := short(ref.Repo()+":"+ref.Tag), short(ref.Repo())
		if !slices.Contains(byImage[k], o) {
			byImage[k] = append(byImage[k], o)
		}
		if !slices.Contains(byRepo[r], o) {
			byRepo[r] = append(byRepo[r], o)
		}
	}
	for i := range findings {
		teams := byImage[findings[i].Image]
		if len(teams) == 0 {
			teams = byRepo[findings[i].Image]
		}
		findings[i].Teams = slices.Sorted(slices.Values(teams))
	}
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

// quietAfter is how long an upstream may go without a release before Goliash says it may be unmaintained.
const quietAfter = 365 * 24 * time.Hour

// QuietSince is when a service's upstream last released, and whether that is over a year ago while Goliash kept
// checking it: its newest publication date when known, else when Goliash first saw a release. An upstream whose
// last check failed, or ran over a week ago, is not quiet: its silence says nothing.
func QuietSince(releases []store.Release, check store.UpstreamCheck, now time.Time) (time.Time, bool) {
	if check.Error != "" || check.At.IsZero() || now.Sub(check.At) > 7*24*time.Hour {
		return time.Time{}, false
	}
	var last time.Time
	for _, r := range releases {
		at := r.PublishedAt
		if at.IsZero() {
			at = r.DiscoveredAt
		}
		if at.After(last) {
			last = at
		}
	}
	return last, !last.IsZero() && now.Sub(last) >= quietAfter
}

// QuietUpstreams finds services whose upstream has released nothing for over a year, while Goliash kept checking
// it: the project may be unmaintained, and the fixes of its next vulnerability may never come. The last release is
// its publication date when known, else when Goliash first saw it; a service Goliash has not watched for a year,
// or whose last check failed, is left out.
func QuietUpstreams(active []store.Instance, releases map[string][]store.Release, checks map[string]store.UpstreamCheck,
	services []store.Service, envName, targetName map[string]string, now time.Time,
) []Finding {
	name := map[string]string{}
	for _, s := range services {
		name[s.ID] = s.Name
	}
	byService := map[string]*Finding{}
	var order []string
	for _, i := range active {
		repo := ParseImage(i.Image).Repo()
		if i.ServiceID == "" || Managed(repo) || PackageRepo(repo) {
			continue
		}
		f := byService[i.ServiceID]
		if f == nil {
			if _, done := byService[i.ServiceID]; done {
				continue
			}
			byService[i.ServiceID] = nil
			last, quiet := QuietSince(releases[i.ServiceID], checks[i.ServiceID], now)
			if !quiet {
				continue
			}
			months := int(now.Sub(last).Hours() / 24 / 30)
			f = &Finding{
				Kind: "quiet-upstream", Severity: "warning",
				Image: strings.TrimPrefix(strings.TrimPrefix(repo, "docker.io/library/"), "docker.io/"),
				Detail: fmt.Sprintf("%s: no new release upstream since %s (%d months): it may be unmaintained, and fixes may not come; "+
					"look for a maintained fork or a replacement", name[i.ServiceID], last.Format("Jan 2006"), months),
			}
			if !IsPublicRegistry(repo) { // most likely built in house: nothing is abandoned, but nothing was rebuilt either
				f.Severity = "info"
				f.Detail = fmt.Sprintf("%s: no new image pushed since %s (%d months): its base image and dependencies are as old; "+
					"a rebuild picks up their fixes", name[i.ServiceID], last.Format("Jan 2006"), months)
			}
			byService[i.ServiceID] = f
			order = append(order, i.ServiceID)
		}
		f.Where = append(f.Where, envName[i.EnvironmentID]+" / "+targetName[i.TargetID]+" / "+i.WorkloadName+" / "+i.ContainerName)
	}
	out := make([]Finding, 0, len(order))
	for _, id := range order {
		sort.Strings(byService[id].Where)
		out = append(out, *byService[id])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == "warning"
		}
		return out[i].Image < out[j].Image
	})
	return out
}
