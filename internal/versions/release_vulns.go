// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Before anyone deploys an upgrade, Goliash reads the image of the version it would move to, so the Updates page
// can say what upgrading fixes ("fixes 3 exploited, brings 2 new") for a release that runs nowhere yet. The server
// reads the images of registries it can; an agent those of the registries it checks, asked by tag
// (release_tags), answering with the digest. The packages go to image_sboms like any image's, the tag's digest to
// release_images, and OSV is asked about them with the running images'.

const (
	releasesPerRepo = 3              // release images read per repository: the most urgent upgrades first
	releasesPerRun  = 2              // release images the server reads per workspace and run (layers are big)
	releaseReread   = 24 * time.Hour // how long a release tag's digest is trusted before it is read again
	requestTTL      = 24 * time.Hour // how long a check asked for keeps being tried
	checkedKeep     = 14 * 24 * time.Hour
)

// releaseTarget is a version an upgrade would move a service to, in its repository, that runs nowhere yet.
type releaseTarget struct{ service, repo, tag string }

// releaseTargets are the versions the open upgrades move to that run nowhere yet, most urgent first.
func releaseTargets(o Overview, active []store.Instance, now time.Time) []releaseTarget {
	running := map[string]bool{}
	for _, in := range active {
		running[ParseImage(in.Image).Repo()+":"+in.Tag] = true
	}
	perRepo := map[string]int{}
	seen := map[string]bool{}
	var out []releaseTarget
	for _, u := range Updates(o, nil, now) {
		repo := o.Refs[u.Service.ID].Repo
		key := repo + ":" + u.Target
		if u.Target == "" || repo == "" || PackageRepo(repo) || Managed(repo) || running[key] || perRepo[repo] >= releasesPerRepo {
			continue
		}
		if !seen[key] {
			seen[key] = true
			perRepo[repo]++
		}
		out = append(out, releaseTarget{u.Service.ID, repo, u.Target})
	}
	return out
}

// releasesToRead are the release tags whose image is not read yet, or was read too long ago, by repository.
func (c *Checker) releasesToRead(ctx context.Context, sc store.Scope, active []store.Instance) (map[string][]string, error) {
	o, err := LoadOverview(ctx, c.store, sc)
	if err != nil {
		return nil, err
	}
	have, err := c.store.ListReleaseImages(ctx, sc)
	if err != nil {
		return nil, err
	}
	now := c.now()
	out := map[string][]string{}
	// Images someone asked to check come first, in a steady order: the agents' configuration changes only when it does.
	for _, r := range have {
		if r.Digest == "" && now.Sub(r.ReadAt) < requestTTL {
			out[r.Repo] = append(out[r.Repo], r.Tag)
		}
	}
	for repo := range out {
		sort.Strings(out[repo])
	}
	for _, t := range releaseTargets(o, active, now) {
		if r, ok := have[t.repo+":"+t.tag]; ok && (r.Digest == "" || now.Sub(r.ReadAt) < releaseReread) {
			continue // asked for above, or read lately
		}
		if !slices.Contains(out[t.repo], t.tag) {
			out[t.repo] = append(out[t.repo], t.tag)
		}
	}
	return out, nil
}

// readReleases reads the packages of the release images the server can read itself, a few per run, and returns
// the image_sboms keys (repo@digest) of every release image of an open upgrade, for OSV.
func (c *Checker) readReleases(ctx context.Context, sc store.Scope, active []store.Instance, auth RegistryAuth,
	sboms map[string]store.ImageSBOM,
) (map[string]bool, error) {
	toRead, err := c.releasesToRead(ctx, sc, active)
	if err != nil {
		return nil, err
	}
	digests, _ := c.tags.(DigestReader)
	packages, _ := c.tags.(PackageReader)
	read := 0
	for repo, tags := range toRead {
		if digests == nil || packages == nil || !auth.Reads(repo) {
			continue // the agents read it, asked by tag
		}
		creds, _ := auth.For(repo)
		for _, tag := range tags {
			if read >= releasesPerRun {
				break
			}
			read++
			digest, err := digests.TagDigest(ctx, repo, tag, creds)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil {
				c.log.DebugContext(ctx, "release image not readable", "repo", repo, "tag", tag, "err", err)
				continue
			}
			key := repo + "@" + digest
			if b, ok := sboms[key]; !ok || b.Error != "" {
				list, err := packages.ImagePackages(ctx, repo, digest, creds)
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				rec := store.ImageSBOM{Repo: repo, Digest: digest, Purls: list.Purls, Source: "packages", GoLinked: list.GoLinked}
				if err != nil {
					rec.Error = err.Error()
				}
				if err := c.store.SetImageSBOM(ctx, sc, rec); err != nil {
					return nil, err
				}
				sboms[key] = rec
			}
			if err := c.store.SetReleaseImage(ctx, sc, store.ReleaseImage{Repo: repo, Tag: tag, Digest: digest}); err != nil {
				return nil, err
			}
		}
	}
	return releaseKeys(ctx, c.store, sc, active, c.now())
}

// releaseKeys are the image_sboms keys of the release images of the open upgrades.
func releaseKeys(ctx context.Context, st *store.Store, sc store.Scope, active []store.Instance, now time.Time) (map[string]bool, error) {
	o, err := LoadOverview(ctx, st, sc)
	if err != nil {
		return nil, err
	}
	have, err := st.ListReleaseImages(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, t := range releaseTargets(o, active, now) {
		if r, ok := have[t.repo+":"+t.tag]; ok && r.Digest != "" {
			out[r.Repo+"@"+r.Digest] = true
		}
	}
	for _, r := range have { // and the images checked lately
		if r.Digest != "" && now.Sub(r.ReadAt) < checkedKeep {
			out[r.Repo+"@"+r.Digest] = true
		}
	}
	return out, nil
}

// rawVulns are the vulnerabilities OSV knows in purls, keyed by CVE, and whether OSV answered for every package.
// Both sides of a release comparison are read this way, so neither carries the running images' distribution
// tracker and VEX filtering alone.
func rawVulns(purls []string, pv map[string]store.PackageVulns, records map[string]store.Vuln, kev map[string]store.Exploited) (map[string]VulnRef, bool) {
	out := map[string]VulnRef{}
	for _, p := range purls {
		for _, id := range pv[p].Vulns {
			key := vulnKey(id, records)
			_, exploited := kev[key]
			out[key] = VulnRef{ID: key, Severity: records[id].Severity, Exploited: exploited}
		}
	}
	answered := true
	for _, p := range purls {
		if _, ok := pv[p]; !ok {
			answered = false
			break
		}
	}
	return out, answered && len(purls) > 0
}

// releaseDeltas fills in the vulnerability delta of upgrades to a version that runs nowhere yet, from the packages
// of its release image and of the image running, as OSV answered for them.
func releaseDeltas(ctx context.Context, st *store.Store, sc store.Scope, o Overview, us []Update) error {
	have, err := st.ListReleaseImages(ctx, sc)
	if err != nil || len(have) == 0 {
		return err
	}
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return err
	}
	sboms, err := st.ListImageSBOMs(ctx, sc)
	if err != nil {
		return err
	}
	runningPurls := map[vulnSide][]string{}
	for _, in := range active {
		if in.IsMain && in.Digest != "" {
			if b := sboms[ParseImage(in.Image).Repo()+"@"+in.Digest]; len(b.Purls) > 0 {
				runningPurls[vulnSide{in.ServiceID, in.EnvironmentID, in.Tag}] = b.Purls
			}
		}
	}
	var wanted []int
	var purls []string
	for i, u := range us {
		if u.Vulns.Known || u.Target == "" {
			continue
		}
		r, ok := have[o.Refs[u.Service.ID].Repo+":"+u.Target]
		run := runningPurls[vulnSide{u.Service.ID, u.Environment.ID, u.Running}]
		if !ok || len(sboms[r.Repo+"@"+r.Digest].Purls) == 0 || len(run) == 0 {
			continue
		}
		wanted = append(wanted, i)
		purls = append(append(purls, sboms[r.Repo+"@"+r.Digest].Purls...), run...)
	}
	if len(wanted) == 0 {
		return nil
	}
	pv, err := st.PackageVulnsOf(ctx, purls)
	if err != nil {
		return err
	}
	records, err := st.ListVulns(ctx)
	if err != nil {
		return err
	}
	kev, _, err := st.KnownExploited(ctx)
	if err != nil {
		return err
	}
	for _, i := range wanted {
		u := &us[i]
		r := have[o.Refs[u.Service.ID].Repo+":"+u.Target]
		newer, okNew := rawVulns(sboms[r.Repo+"@"+r.Digest].Purls, pv, records, kev)
		running, okRun := rawVulns(runningPurls[vulnSide{u.Service.ID, u.Environment.ID, u.Running}], pv, records, kev)
		if !okNew || !okRun {
			continue // OSV has not answered for every package yet
		}
		d := VulnDelta{Known: true, Unreleased: true}
		for id, v := range running {
			if _, still := newer[id]; !still {
				d.Fixed = append(d.Fixed, v)
			}
		}
		for id, v := range newer {
			if _, had := running[id]; !had {
				d.New = append(d.New, v)
			}
		}
		sortVulnRefs(d.Fixed)
		sortVulnRefs(d.New)
		u.Vulns = d
	}
	return nil
}
