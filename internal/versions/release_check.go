// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/store"
)

// States of an image check, in the order an image goes through them.
const (
	CheckRequested  = "requested"  // asked for: the server, or an agent, reads the image within minutes
	CheckReading    = "reading"    // its digest is known, its packages not yet
	CheckUnreadable = "unreadable" // its packages could not be read
	CheckAsking     = "asking"     // its packages are known, OSV has not answered for all of them yet
	CheckReady      = "ready"
)

// ReleaseCheck is what Goliash knows of one image, running or not: its known vulnerabilities, and what moving to it
// does to those of the versions of its repository that run.
type ReleaseCheck struct {
	Image, Repo, Tag string
	Running          []string // the environments running it
	ByAgent          bool     // an agent reads it, with its credentials
	State            string
	Error            string // why its packages could not be read
	Packages         int
	Total, Critical  int
	Exploited        []VulnRef
	Against          []ReleaseComparison // the versions of the repository that run, newest first
}

// ReleaseComparison is what moving one running version to the image checked does to its known vulnerabilities.
type ReleaseComparison struct {
	Version, Env          string
	FixedExploited, Still []VulnRef // the running version's exploited vulnerabilities the image fixes, and keeps
	Fixed, New            int
}

// CheckRelease checks the image ref (registry/repository:tag): from what runs when it does, else from its image in
// the registry, which it asks to be read when it was not yet. Call it again until State is CheckReady.
func CheckRelease(ctx context.Context, st *store.Store, sc store.Scope, ref string) (ReleaseCheck, error) {
	img := ParseImage(strings.TrimSpace(ref))
	out := ReleaseCheck{Image: ShortImage(ref), Repo: img.Repo(), Tag: img.Tag, State: CheckRequested}
	o, err := LoadOverview(ctx, st, sc)
	if err != nil {
		return out, err
	}
	out.ByAgent = AgentReads(o, out.Repo)
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return out, err
	}
	sboms, err := st.ListImageSBOMs(ctx, sc)
	if err != nil {
		return out, err
	}
	// The versions of the repository that run, each with the packages of its image.
	type running struct {
		tag, env string
		purls    []string
	}
	var runs []running
	var purls []string
	for _, in := range active {
		if !in.IsMain || ParseImage(in.Image).Repo() != out.Repo {
			continue
		}
		env := o.Envs[in.EnvironmentID].Name
		b := sboms[out.Repo+"@"+in.Digest]
		if in.Tag == out.Tag {
			if !slices.Contains(out.Running, env) {
				out.Running = append(out.Running, env)
			}
			if len(b.Purls) > 0 && purls == nil {
				purls = b.Purls
			}
			continue
		}
		if len(b.Purls) > 0 && !slices.ContainsFunc(runs, func(r running) bool { return r.tag == in.Tag && r.env == env }) {
			runs = append(runs, running{in.Tag, env, b.Purls})
		}
	}
	if purls == nil {
		have, err := st.ListReleaseImages(ctx, sc)
		if err != nil {
			return out, err
		}
		r, ok := have[out.Repo+":"+out.Tag]
		switch {
		case !ok:
			return out, st.RequestReleaseImage(ctx, sc, out.Repo, out.Tag)
		case r.Digest == "":
			return out, nil
		}
		b := sboms[out.Repo+"@"+r.Digest]
		if len(b.Purls) == 0 {
			out.State, out.Error = CheckReading, b.Error
			if b.Error != "" {
				out.State = CheckUnreadable
			}
			return out, nil
		}
		purls = b.Purls
	}
	out.Packages = len(purls)
	all := slices.Clone(purls)
	for _, r := range runs {
		all = append(all, r.purls...)
	}
	pv, err := st.PackageVulnsOf(ctx, all)
	if err != nil {
		return out, err
	}
	records, err := st.ListVulns(ctx)
	if err != nil {
		return out, err
	}
	kev, _, err := st.KnownExploited(ctx)
	if err != nil {
		return out, err
	}
	mine, answered := rawVulns(purls, pv, records, kev)
	if !answered {
		out.State = CheckAsking
		return out, nil
	}
	out.State, out.Total = CheckReady, len(mine)
	for _, v := range mine {
		switch {
		case v.Exploited:
			out.Exploited = append(out.Exploited, v)
		case strings.EqualFold(v.Severity, "CRITICAL"):
			out.Critical++
		}
	}
	sortVulnRefs(out.Exploited)
	for _, r := range runs {
		theirs, ok := rawVulns(r.purls, pv, records, kev)
		if !ok {
			continue
		}
		c := ReleaseComparison{Version: r.tag, Env: r.env}
		for id, v := range theirs {
			_, still := mine[id]
			switch {
			case still && v.Exploited:
				c.Still = append(c.Still, v)
			case !still:
				c.Fixed++
				if v.Exploited {
					c.FixedExploited = append(c.FixedExploited, v)
				}
			}
		}
		for id := range mine {
			if _, had := theirs[id]; !had {
				c.New++
			}
		}
		sortVulnRefs(c.FixedExploited)
		sortVulnRefs(c.Still)
		out.Against = append(out.Against, c)
	}
	sort.SliceStable(out.Against, func(i, j int) bool {
		a, okA := ParseVersion(out.Against[i].Version)
		b, okB := ParseVersion(out.Against[j].Version)
		if okA && okB {
			return a.Compare(b) > 0
		}
		return out.Against[i].Version > out.Against[j].Version
	})
	return out, nil
}

// LooksLikeImage reports whether a search is an image reference to check (registry or repository, and a tag),
// rather than words to look for.
func LooksLikeImage(q string) bool {
	q = strings.TrimSpace(q)
	if q == "" || strings.ContainsAny(q, " \t") {
		return false
	}
	slash, colon := strings.LastIndex(q, "/"), strings.LastIndex(q, ":")
	return slash > 0 && colon > slash+1 && colon < len(q)-1
}
