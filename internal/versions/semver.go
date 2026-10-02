// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Version is a tag read as a version: "v1.27.2", "15.6-alpine", "2.0.0-rc.1".
type Version struct {
	Raw     string
	Parts   []int  // 1 to 4 numeric components
	Pre     string // prerelease: rc.1, beta2, alpha …
	Variant string // build flavour that is not a prerelease: alpine, bookworm-slim …
}

var (
	versionRe = regexp.MustCompile(`^[vV]?(\d+(?:\.\d+){0,3})(?:[-_+](.+))?$`)
	preRe     = regexp.MustCompile(`^(?i)(alpha|beta|rc|pre|preview|dev|snapshot|canary|nightly|next|m)[.]?\d*$`)
)

// ParseVersion reads a tag as a version. ok is false for tags such as "latest",
// "stable" or "sha-1a2b3c".
func ParseVersion(tag string) (Version, bool) {
	m := versionRe.FindStringSubmatch(tag)
	if m == nil {
		return Version{}, false
	}
	v := Version{Raw: tag}
	for _, p := range strings.Split(m[1], ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n > 1_000_000 { // dates and build numbers like 20240101 are not versions
			return Version{}, false
		}
		v.Parts = append(v.Parts, n)
	}
	if m[2] != "" {
		tokens := strings.FieldsFunc(m[2], func(r rune) bool { return r == '-' })
		i := 0
		for i < len(tokens) && preRe.MatchString(tokens[i]) {
			i++
		}
		// "rc.1" splits as one token; "rc" "1" style is joined back.
		v.Pre = strings.Join(tokens[:i], "-")
		v.Variant = strings.Join(tokens[i:], "-")
	}
	return v, true
}

func (v Version) part(i int) int {
	if i < len(v.Parts) {
		return v.Parts[i]
	}
	return 0
}

// Compare orders versions: -1, 0 or 1. A prerelease sorts before its release.
func (v Version) Compare(o Version) int {
	for i := range 4 {
		if a, b := v.part(i), o.part(i); a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	return comparePre(v.Pre, o.Pre)
}

func comparePre(a, b string) int {
	na, nb := trailingNumber(a), trailingNumber(b)
	sa, sb := strings.TrimRight(a, "0123456789."), strings.TrimRight(b, "0123456789.")
	if sa != sb {
		return strings.Compare(strings.ToLower(sa), strings.ToLower(sb)) // alpha < beta < rc
	}
	switch {
	case na < nb:
		return -1
	case na > nb:
		return 1
	}
	return 0
}

func trailingNumber(s string) int {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	n, _ := strconv.Atoi(s[i:])
	return n
}

// Jump is the size of a version step.
type Jump string

// Jumps, smallest first.
const (
	JumpNone  Jump = ""
	JumpPatch Jump = "patch"
	JumpMinor Jump = "minor"
	JumpMajor Jump = "major"
)

func (j Jump) rank() int {
	switch j {
	case JumpPatch:
		return 1
	case JumpMinor:
		return 2
	case JumpMajor:
		return 3
	}
	return 0
}

// AtLeast reports whether j is as large as other.
func (j Jump) AtLeast(other Jump) bool { return j.rank() >= other.rank() && j != JumpNone }

// JumpTo returns how far "to" is ahead of v; JumpNone when it is not newer.
func (v Version) JumpTo(to Version) Jump {
	switch {
	case v.Compare(to) >= 0:
		return JumpNone
	case to.part(0) != v.part(0):
		return JumpMajor
	case to.part(1) != v.part(1):
		return JumpMinor
	}
	return JumpPatch
}

// Policy is a service's version policy, stored as JSON on the service.
type Policy struct {
	TagFilter  string `json:"tag_filter,omitempty"` // regexp tags must match; default: same shape as the running tag
	Track      Jump   `json:"track,omitempty"`      // smallest jump worth an alert; default patch
	PinMajor   *int   `json:"pin_major,omitempty"`  // newer majors are information, not alerts
	Prerelease bool   `json:"prerelease,omitempty"` // consider alpha, beta, rc
}

// ParsePolicy reads a stored policy; empty input is the default policy.
func ParsePolicy(raw []byte) (Policy, error) {
	var p Policy
	if len(raw) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if p.TagFilter != "" {
		if _, err := regexp.Compile(p.TagFilter); err != nil {
			return p, fmt.Errorf("tag_filter: %w", err)
		}
	}
	switch p.Track {
	case JumpNone, JumpPatch, JumpMinor, JumpMajor:
	default:
		return p, fmt.Errorf("track must be patch, minor or major, not %q", p.Track)
	}
	return p, nil
}

// Upstream is what the registry offers compared to a running tag.
type Upstream struct {
	Latest       Version // newest acceptable tag within the pinned major (or overall when unpinned)
	LatestAny    Version // newest acceptable tag ignoring the pin
	HasLatest    bool
	HasLatestAny bool
}

// Candidates filters tags to those comparable with the running tag under the policy,
// sorted newest first.
func Candidates(tags []string, running string, p Policy) []Version {
	cur, curOK := ParseVersion(running)
	var filter *regexp.Regexp
	if p.TagFilter != "" {
		filter, _ = regexp.Compile(p.TagFilter)
	}
	var out []Version
	for _, t := range tags {
		if filter != nil && !filter.MatchString(t) {
			continue
		}
		v, ok := ParseVersion(t)
		if !ok {
			continue
		}
		if v.Pre != "" && !p.Prerelease && (!curOK || cur.Pre == "") {
			continue
		}
		if filter == nil && curOK {
			// Without a filter, only compare like with like: same flavour, same precision.
			if v.Variant != cur.Variant || len(v.Parts) != len(cur.Parts) {
				continue
			}
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Compare(out[j]) > 0 })
	return out
}

// Latest finds the newest acceptable versions among tags for a running tag.
func Latest(tags []string, running string, p Policy) Upstream {
	var u Upstream
	for _, v := range Candidates(tags, running, p) {
		if !u.HasLatestAny {
			u.LatestAny, u.HasLatestAny = v, true
		}
		if p.PinMajor == nil || v.part(0) == *p.PinMajor {
			u.Latest, u.HasLatest = v, true
			break
		}
	}
	return u
}

// Lagging reports whether the running tag is behind the newest acceptable version by
// at least the tracked jump, and by how much.
func Lagging(running string, u Upstream, p Policy) (Jump, bool) {
	cur, ok := ParseVersion(running)
	if !ok || !u.HasLatest {
		return JumpNone, false
	}
	j := cur.JumpTo(u.Latest)
	track := p.Track
	if track == JumpNone {
		track = JumpPatch
	}
	return j, j.AtLeast(track)
}
