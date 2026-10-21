// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/store"
)

// WatchMatch is a service a security watch covers, and why: its name, an image or workload it runs, a GitOps
// application that deploys it, or a package in its running images' SBOMs.
type WatchMatch struct {
	ServiceID string
	Service   string
	Why       string // "runs docker.io/library/php", "package php8.2-cli"
	Running   string // the version it runs (in its last environment)
}

// WatchMatches finds, for each term, the services it covers. Every word of a term must match one of what a
// service is or runs, as in search.
func WatchMatches(ctx context.Context, st *store.Store, sc store.Scope, o Overview, terms []string) (map[string][]WatchMatch, error) {
	keys := map[string][]string{} // service ID -> what it runs
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return nil, err
	}
	for _, in := range active {
		if in.IsMain && in.ServiceID != "" {
			keys[in.ServiceID] = append(keys[in.ServiceID], "runs "+ParseImage(in.Image).Repo(), "runs "+in.Image, "workload "+in.WorkloadName)
		}
	}
	if apps, err := GitOpsApps(ctx, st, sc, o); err == nil {
		for _, a := range apps {
			keys[a.ServiceID] = append(keys[a.ServiceID], "deployed by "+a.App)
		}
	}
	if rep, err := LoadLicenseReport(ctx, st, sc, ""); err == nil {
		for _, p := range rep.Packages {
			for _, u := range p.Where {
				keys[u.ServiceID] = append(keys[u.ServiceID], "package "+p.Name+" "+p.Version)
			}
		}
	}
	out := map[string][]WatchMatch{}
	for _, term := range terms {
		match := Matcher(term)
		for _, row := range o.Matrix.Rows {
			svc := row.Service
			why := ""
			if match(svc.Name) {
				why = "its name"
			} else {
				for _, k := range keys[svc.ID] {
					if match(strings.SplitN(k, " ", 2)[1]) {
						why = k
						break
					}
				}
			}
			if why == "" {
				continue
			}
			out[term] = append(out[term], WatchMatch{ServiceID: svc.ID, Service: svc.Name, Why: why, Running: o.Refs[svc.ID].Tag})
		}
		sort.Slice(out[term], func(i, j int) bool { return out[term][i].Service < out[term][j].Service })
	}
	return out, nil
}
