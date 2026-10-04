// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Promotion is a version waiting to be promoted: an environment runs something newer
// than the environment after it (an open env drift), with the releases in between.
type Promotion struct {
	Service  store.Service
	From     store.Environment // where the newer version runs
	To       store.Environment // the environment behind
	Version  string            // waiting in From
	Running  string            // running in To
	Since    time.Time         // waiting since
	Releases []store.Release   // releases after Running up to Version, newest first
}

// Promotions lists the versions waiting to be promoted, longest waiting first.
func Promotions(ctx context.Context, st *store.Store, sc store.Scope, o Overview) ([]Promotion, error) {
	envByName := map[string]store.Environment{}
	for _, e := range o.Envs {
		envByName[e.Name] = e
	}
	var out []Promotion
	for _, ds := range o.Drifts {
		for _, d := range ds {
			if d.Kind != "env" {
				continue
			}
			var det DriftDetail
			if json.Unmarshal(d.Detail, &det) != nil {
				continue
			}
			p := Promotion{
				Service: o.Services[d.ServiceID], From: envByName[det.OtherIn], To: o.Envs[d.EnvironmentID],
				Version: det.Other, Running: det.Running, Since: d.Since,
			}
			releases, err := st.ListReleases(ctx, sc, d.ServiceID)
			if err != nil {
				return nil, err
			}
			p.Releases = between(releases, det.Running, det.Other)
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Since.Equal(out[j].Since) {
			return out[i].Since.Before(out[j].Since)
		}
		return out[i].Service.Name < out[j].Service.Name
	})
	return out, nil
}

// between returns the releases newer than from and up to to, newest first, keeping
// the variant of to (1.27.2-alpine compares with -alpine releases only).
func between(releases []store.Release, from, to string) []store.Release {
	lo, okLo := ParseVersion(from)
	hi, okHi := ParseVersion(to)
	if !okLo || !okHi {
		return nil
	}
	type rv struct {
		r store.Release
		v Version
	}
	var in []rv
	for _, r := range releases {
		v, ok := ParseVersion(r.Version)
		if !ok || v.Variant != hi.Variant || v.Pre != "" && hi.Pre == "" {
			continue
		}
		if v.Compare(lo) > 0 && v.Compare(hi) <= 0 {
			in = append(in, rv{r, v})
		}
	}
	sort.Slice(in, func(i, j int) bool { return in[i].v.Compare(in[j].v) > 0 })
	out := make([]store.Release, len(in))
	for i, x := range in {
		out[i] = x.r
	}
	return out
}
