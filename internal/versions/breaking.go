// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"regexp"
	"sort"
	"strings"
)

// breakingLine finds the lines of release notes that say something breaks: "Breaking change", BREAKING, a
// conventional commit with "!" ("feat(api)!: …"), removed support, a migration or manual step required, an
// incompatible change.
var breakingLine = regexp.MustCompile(`(?i)\bbreaking[ -]?changes?\b|` +
	`(?-i:\bBREAKING\b)|` +
	`^\s*[-*+]?\s*(\*\*)?[a-z]+(\([^)]*\))?!:|` +
	`\b(removed|drops?|dropped|dropping|ends?) support for\b|\bno longer (supports?|supported|works|accepts?|compatible)\b|` +
	`\b(migration|manual (step|action|intervention)|action) (is |are )?required\b|\brequires? (a )?(manual )?migration\b|` +
	`\b(backwards?|backward)[- ]incompatible\b|\bincompatible change`)

// notBreaking is a line that says nothing breaks.
var notBreaking = regexp.MustCompile(`(?i)\bno (known )?breaking\b|\bwithout (any )?breaking\b|\bnon[- ]breaking\b`)

// headingOnly is a line that is only a heading or a label: "## Breaking changes", "**BREAKING:**".
var headingOnly = regexp.MustCompile(`(?i)^[#*_\s]*(⚠️\s*)?(breaking[ -]?changes?|breaking)[:*_\s]*$`)

// breakingShown bounds how many lines BreakingIn quotes, and breakingWidth how long each may be.
const (
	breakingShown = 2
	breakingWidth = 140
)

// BreakingIn quotes what release notes say breaks: a line or two, " · " between them; "" when they say nothing of
// it. A heading ("## Breaking changes") quotes the line under it.
func BreakingIn(notes string) string {
	lines := strings.Split(strings.ReplaceAll(notes, "\r\n", "\n"), "\n")
	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(lines) && len(out) < breakingShown; i++ {
		line := lines[i]
		if !breakingLine.MatchString(line) || notBreaking.MatchString(line) {
			continue
		}
		if headingOnly.MatchString(line) {
			for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
				i++
			}
			if i+1 >= len(lines) {
				break
			}
			i++
			line = lines[i]
		}
		if q := quoteLine(line); q != "" && !seen[q] {
			seen[q] = true
			out = append(out, q)
		}
	}
	return strings.Join(out, " · ")
}

// markdownNoise is what a quoted line loses: list markers, heading marks, emphasis, links' targets.
var (
	markdownLead  = regexp.MustCompile(`^[\s>*+#-]+`)
	markdownLink  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	markdownStyle = regexp.MustCompile("[*_`]{1,3}")
)

// quoteLine is a line of notes as plain, bounded text.
func quoteLine(line string) string {
	s := markdownLink.ReplaceAllString(line, "$1")
	s = markdownStyle.ReplaceAllString(s, "")
	s = strings.TrimSpace(markdownLead.ReplaceAllString(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > breakingWidth {
		s = strings.TrimSpace(string(r[:breakingWidth-1])) + "…"
	}
	return s
}

// breakingUpTo is what the releases after running, up to and including target, say breaks: "2.0.0: Removed …", the
// oldest first.
func breakingUpTo(o Overview, svc, running, target string) []string {
	from, ok1 := ParseVersion(running)
	to, ok2 := ParseVersion(target)
	if !ok1 || !ok2 {
		return nil
	}
	type entry struct {
		v    Version
		text string
	}
	var es []entry
	for key, text := range o.ReleaseBreaking {
		id, ver, _ := strings.Cut(key, "|")
		if id != svc {
			continue
		}
		v, ok := ParseVersion(ver)
		if !ok || v.Compare(from) <= 0 || v.Compare(to) > 0 {
			continue
		}
		es = append(es, entry{v, ver + ": " + text})
	}
	sort.Slice(es, func(i, j int) bool { return es[i].v.Compare(es[j].v) < 0 })
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.text
	}
	return out
}
