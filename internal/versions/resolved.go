// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A deploy that replaces an image closes vulnerabilities only if the new image is really without them. Goliash
// compares the packages of the image a service ran in an environment with those of the image that replaced it:
// a vulnerability gone because its package is at another version without it is fixed; one gone with its package
// is removed; one gone while the new image's packages are not known yet is not verified, only no longer seen.

// Resolution is what the last deploy of a service in an environment did to the vulnerabilities of the image it
// replaced.
type Resolution struct {
	ServiceID, Service, EnvironmentID, Env string
	From, To                               string    // the images: nginx:1.25.3 → nginx:1.27.2
	At                                     time.Time // when the old image stopped running
	Verified                               bool      // the new image's packages are known
	Fixed, Removed, Unverified             []ResolvedVuln
	Still                                  int // the old image's vulnerabilities the new one still has
}

// ResolvedVuln is a vulnerability the old image had: its package and version there, and in the new image.
type ResolvedVuln struct {
	CVE, Severity string
	Exploited     bool
	Package       string // libbrotli1
	From, To      string // 1.0.9-2+b6 → 1.0.9-2+deb12u1; To is empty when the package is gone
}

// Closed counts the vulnerabilities the deploy closed for sure: fixed or removed with their package.
func (r Resolution) Closed() int { return len(r.Fixed) + len(r.Removed) }

// Resolutions are the last deploy of each service and environment since since that replaced an image whose
// vulnerabilities are known, newest first. Only main containers count, and deploys that changed nothing are left
// out.
func Resolutions(ctx context.Context, st *store.Store, sc store.Scope, since time.Time) ([]Resolution, error) {
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return nil, err
	}
	removed, err := st.ListRemovedInstances(ctx, sc, since)
	if err != nil {
		return nil, err
	}
	type place struct{ service, env string }
	type image struct{ key, name string }
	now := map[place][]image{}
	for _, in := range active {
		if k := in.PackagesKey(ParseImage(in.Image).Repo()); in.IsMain && in.ServiceID != "" && k != "" {
			now[place{in.ServiceID, in.EnvironmentID}] = append(now[place{in.ServiceID, in.EnvironmentID}], image{k, in.Image})
		}
	}
	// The image each place ran before its current one: the last removed that is not running there now.
	before := map[place]store.Instance{}
	for _, in := range removed {
		p := place{in.ServiceID, in.EnvironmentID}
		k := in.PackagesKey(ParseImage(in.Image).Repo())
		if !in.IsMain || k == "" || len(now[p]) == 0 || containsKey(now[p], k, func(i image) string { return i.key }) {
			continue
		}
		if prev, ok := before[p]; !ok || in.RemovedAt.After(prev.RemovedAt) {
			before[p] = in
		}
	}
	if len(before) == 0 {
		return nil, nil
	}
	sboms, err := st.ListImageSBOMs(ctx, sc)
	if err != nil {
		return nil, err
	}
	var purls []string
	for p, old := range before {
		purls = append(purls, sboms[old.PackagesKey(ParseImage(old.Image).Repo())].Purls...)
		for _, im := range now[p] {
			purls = append(purls, sboms[im.key].Purls...)
		}
	}
	pv, err := st.PackageVulnsOf(ctx, purls)
	if err != nil {
		return nil, err
	}
	records, err := recordsOf(ctx, st, pv)
	if err != nil {
		return nil, err
	}
	kev, _, err := st.KnownExploited(ctx)
	if err != nil {
		return nil, err
	}
	services, err := st.ListServices(ctx, sc)
	if err != nil {
		return nil, err
	}
	envs, err := st.ListEnvironments(ctx, sc)
	if err != nil {
		return nil, err
	}
	svcName, envName := map[string]string{}, map[string]string{}
	for _, s := range services {
		svcName[s.ID] = s.Name
	}
	for _, e := range envs {
		envName[e.ID] = e.Name
	}

	var out []Resolution
	for p, old := range before {
		oldSBOM := sboms[old.PackagesKey(ParseImage(old.Image).Repo())]
		if len(oldSBOM.Purls) == 0 {
			continue // what it had is not known: nothing to say closed
		}
		r := Resolution{
			ServiceID: p.service, Service: svcName[p.service], EnvironmentID: p.env, Env: envName[p.env],
			From: ShortImage(old.Image), At: old.RemovedAt,
		}
		var names []string
		newPkgs := map[string]string{} // package name -> its version in the new image
		asked := map[string]bool{}     // package name -> OSV answered for its version in the new image
		newVulns := map[string]bool{}
		for _, im := range now[p] {
			names = append(names, ShortImage(im.name))
			b := sboms[im.key]
			r.Verified = r.Verified || len(b.Purls) > 0
			for _, purl := range b.Purls {
				name, version, _ := strings.Cut(purlName(purl), " ")
				newPkgs[name] = version
				_, asked[name] = pv[purl]
				for _, id := range pv[purl].Vulns {
					newVulns[vulnKey(id, records)] = true
				}
			}
		}
		r.To = strings.Join(dedupe(names), ", ")
		seen := map[string]bool{}
		for _, purl := range oldSBOM.Purls {
			name, version, _ := strings.Cut(purlName(purl), " ")
			for _, id := range pv[purl].Vulns {
				key := vulnKey(id, records)
				if seen[key+"|"+name] {
					continue
				}
				seen[key+"|"+name] = true
				if newVulns[key] {
					r.Still++
					continue
				}
				v := ResolvedVuln{CVE: key, Severity: records[id].Severity, Package: name, From: version}
				_, v.Exploited = kev[key]
				switch to, there := newPkgs[name]; {
				case !r.Verified, there && !asked[name]:
					v.To = to
					r.Unverified = append(r.Unverified, v)
				case there:
					v.To = to
					r.Fixed = append(r.Fixed, v)
				default:
					r.Removed = append(r.Removed, v)
				}
			}
		}
		if r.Closed()+len(r.Unverified) == 0 {
			continue
		}
		for _, l := range [][]ResolvedVuln{r.Fixed, r.Removed, r.Unverified} {
			sortResolved(l)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// vulnKey is the CVE an advisory is about, as findings are keyed: the ID itself when it names none.
func vulnKey(id string, records map[string]store.Vuln) string {
	if c := CVEOf(id); c != "" {
		return c
	}
	for _, a := range records[id].Aliases {
		if c := CVEOf(a); c != "" {
			return c
		}
	}
	return id
}

// sortResolved puts exploited, then the most severe, first.
func sortResolved(l []ResolvedVuln) {
	sort.SliceStable(l, func(i, j int) bool {
		if l[i].Exploited != l[j].Exploited {
			return l[i].Exploited
		}
		if a, b := severityRank(l[i].Severity), severityRank(l[j].Severity); a != b {
			return a > b
		}
		return l[i].CVE > l[j].CVE
	})
}

func containsKey[T any](l []T, k string, key func(T) string) bool {
	for _, x := range l {
		if key(x) == k {
			return true
		}
	}
	return false
}

func dedupe(l []string) []string {
	sort.Strings(l)
	out := l[:0]
	for i, s := range l {
		if i == 0 || s != l[i-1] {
			out = append(out, s)
		}
	}
	return out
}
