// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"fmt"
	"strings"
)

// Unknown is why the newest release of a running service is not known, so how far behind it runs cannot be told,
// and what would let Goliash tell.
type Unknown struct {
	Short  string // a few words, for a table: "not a version", "check failed"…
	Reason string
	Fix    string
	Where  string // where the fix is made: "registries" (credentials), "policy" (the tag filter), "upstream" or ""
}

// UnknownService is a service whose newest release is unknown, with why.
type UnknownService struct {
	Service string
	Repo    string // the repository it runs from, when it has one
	Unknown
}

// WhyUnknown says why the service's newest release is not known; ok is false when it is known (or the service
// does not run).
func WhyUnknown(o Overview, serviceID string) (Unknown, bool) {
	if up, has := o.Upstreams[serviceID]; has && up.HasLatest {
		return Unknown{}, false
	}
	ref, runs := o.Refs[serviceID]
	if !runs {
		return Unknown{}, false
	}
	svc := o.Services[serviceID]
	switch {
	case ref.Repo == "":
		return Unknown{
			Short:  "no registry image",
			Reason: "It runs no image Goliash can look up in a registry.",
			Fix:    "Set the repository its releases are published to as the service's upstream.",
			Where:  "upstream",
		}, true
	case ref.Tag != "" && !isVersion(ref.Tag):
		return Unknown{
			Short:  "not a version",
			Reason: fmt.Sprintf("It runs the tag %q, which is not a version: there is nothing to compare the releases with.", ref.Tag),
			Fix:    "Deploy a version tag. A moving tag is resolved when the registry also lists the same image under a version tag.",
		}, true
	}
	if msg := o.CheckErrs[serviceID]; msg != "" {
		u := Unknown{Short: "check failed", Reason: "The last look at " + ref.Repo + " failed: " + msg + "."}
		low := strings.ToLower(msg)
		switch {
		case strings.Contains(low, "denied") || strings.Contains(low, "unauthorized") || strings.Contains(low, "401") || strings.Contains(low, "403"):
			u.Fix, u.Where = "Give Goliash read access to "+RegistryHost(ref.Repo)+": registry credentials on the server, or on the agents.", "registries"
		case strings.Contains(low, "not found") || strings.Contains(low, "name_unknown") || strings.Contains(low, "404"):
			u.Fix, u.Where = "The repository is not on the registry under that name. For an image built where it runs, set the repository it is published to as the upstream.", "upstream"
		default:
			u.Fix = "Check again from the service page; a passing registry outage clears by itself."
		}
		return u, true
	}
	if CheckedByAgent(svc, ref.Repo, o.Registries) && o.TagCount[serviceID] == 0 {
		return Unknown{
			Short:  "checked by agent",
			Reason: ref.Repo + " is private: the agents check it, and none has reported its tags yet.",
			Fix:    "Give the agents (or the server) credentials for " + RegistryHost(ref.Repo) + ".",
			Where:  "registries",
		}, true
	}
	if o.CheckedAt[serviceID].IsZero() {
		return Unknown{Short: "not checked yet", Reason: "Goliash has not looked at " + ref.Repo + " yet.", Fix: "The first look runs within minutes; or check now from the service page."}, true
	}
	if up := o.Upstreams[serviceID]; up.HasLatestAny {
		return Unknown{
			Short:  "none in pinned major",
			Reason: fmt.Sprintf("The version policy pins major %d, and no release of it is in %s (the newest is %s).", pinned(o.Policies[serviceID]), ref.Repo, up.LatestAny.Raw),
			Fix:    "Change or remove the pinned major in the version policy.",
			Where:  "policy",
		}, true
	}
	if o.TagCount[serviceID] == 0 {
		return Unknown{Short: "no tags", Reason: "The registry lists no tags for " + ref.Repo + "."}, true
	}
	return Unknown{
		Short:  "no comparable tag",
		Reason: fmt.Sprintf("None of the %d tags of %s is comparable with %q.", o.TagCount[serviceID], ref.Repo, ref.Tag),
		Fix:    "Set a tag filter in the version policy that matches the tags of its releases.",
		Where:  "policy",
	}, true
}

// isVersion tells whether a tag reads as a version Goliash can order.
func isVersion(tag string) bool {
	_, ok := ParseVersion(tag)
	return ok
}

func pinned(p Policy) int {
	if p.PinMajor == nil {
		return 0
	}
	return *p.PinMajor
}
