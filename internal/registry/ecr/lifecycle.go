// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package ecr

import (
	"encoding/json"
	"path"
	"sort"
	"strings"
	"time"
)

// lifecyclePolicy is an ECR lifecycle policy, as much of it as says what it expires.
type lifecyclePolicy struct {
	Rules []struct {
		RulePriority int `json:"rulePriority"`
		Selection    struct {
			TagStatus      string   `json:"tagStatus"` // tagged, untagged or any
			TagPrefixList  []string `json:"tagPrefixList"`
			TagPatternList []string `json:"tagPatternList"`
			CountType      string   `json:"countType"` // imageCountMoreThan or sinceImagePushed
			CountUnit      string   `json:"countUnit"` // days
			CountNumber    int      `json:"countNumber"`
		} `json:"selection"`
		Action struct {
			Type string `json:"type"` // expire
		} `json:"action"`
	} `json:"rules"`
}

// Expiries says, by image digest, when a lifecycle policy deletes each image it will: now for one a count rule
// leaves out (ECR acts within a day), the push time plus the rule's days for a sinceImagePushed rule. Rules apply
// by priority, lowest first, and an image a rule's tags select is not considered by the rules after it, whether it
// expires or not, as ECR does. A policy that cannot be read expires nothing.
func Expiries(policy string, images []Image, now time.Time) map[string]time.Time {
	var p lifecyclePolicy
	if policy == "" || json.Unmarshal([]byte(policy), &p) != nil {
		return nil
	}
	sort.SliceStable(p.Rules, func(i, j int) bool { return p.Rules[i].RulePriority < p.Rules[j].RulePriority })
	claimed := map[string]bool{}
	out := map[string]time.Time{}
	for _, r := range p.Rules {
		sel := r.Selection
		var picked []Image
		for _, img := range images {
			if claimed[img.Digest] || !selects(sel.TagStatus, sel.TagPrefixList, sel.TagPatternList, img.Tags) {
				continue
			}
			claimed[img.Digest] = true
			picked = append(picked, img)
		}
		if r.Action.Type != "" && r.Action.Type != "expire" {
			continue
		}
		switch sel.CountType {
		case "imageCountMoreThan":
			sort.SliceStable(picked, func(i, j int) bool { return picked[i].PushedAt.After(picked[j].PushedAt) })
			for i, img := range picked {
				if i >= sel.CountNumber {
					out[img.Digest] = now
				}
			}
		case "sinceImagePushed":
			days := time.Duration(sel.CountNumber) * 24 * time.Hour
			for _, img := range picked {
				if !img.PushedAt.IsZero() {
					out[img.Digest] = img.PushedAt.Add(days)
				}
			}
		}
	}
	return out
}

// selects tells whether a rule's tag selection takes an image with these tags: every prefix (or every pattern)
// must match one of its tags.
func selects(status string, prefixes, patterns []string, tags []string) bool {
	switch status {
	case "untagged":
		return len(tags) == 0
	case "any":
		return true
	case "tagged":
		if len(tags) == 0 {
			return false
		}
		for _, p := range prefixes {
			if !anyTag(tags, func(t string) bool { return strings.HasPrefix(t, p) }) {
				return false
			}
		}
		for _, p := range patterns {
			if !anyTag(tags, func(t string) bool { ok, _ := path.Match(p, t); return ok }) {
				return false
			}
		}
		return len(prefixes) > 0 || len(patterns) > 0
	}
	return false
}

func anyTag(tags []string, f func(string) bool) bool {
	for _, t := range tags {
		if f(t) {
			return true
		}
	}
	return false
}
