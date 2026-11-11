// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package ecr

import (
	"testing"
	"time"
)

func TestExpiries(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return now.AddDate(0, 0, -d) }
	images := []Image{
		{Digest: "sha256:a", Tags: []string{"f00d1e5"}, PushedAt: day(1)},
		{Digest: "sha256:b", Tags: []string{"bca73851"}, PushedAt: day(8)},
		{Digest: "sha256:c", Tags: []string{"9e1a2b3"}, PushedAt: day(20)},
		{Digest: "sha256:d", Tags: []string{"release-1.4", "prod"}, PushedAt: day(200)},
		{Digest: "sha256:e", PushedAt: day(3)}, // untagged
	}
	// Releases stay; untagged go after a day; of the rest, the newest two stay.
	policy := `{"rules":[
		{"rulePriority":1,"selection":{"tagStatus":"tagged","tagPrefixList":["release-"],"countType":"imageCountMoreThan","countNumber":100},"action":{"type":"expire"}},
		{"rulePriority":2,"selection":{"tagStatus":"untagged","countType":"sinceImagePushed","countUnit":"days","countNumber":1},"action":{"type":"expire"}},
		{"rulePriority":3,"selection":{"tagStatus":"any","countType":"imageCountMoreThan","countNumber":2},"action":{"type":"expire"}}]}`
	got := Expiries(policy, images, now)
	if _, ok := got["sha256:d"]; ok {
		t.Error("a release a higher rule keeps was expired by a lower one")
	}
	if !got["sha256:e"].Equal(day(2)) {
		t.Errorf("untagged: %v", got["sha256:e"])
	}
	if _, ok := got["sha256:a"]; ok {
		t.Error("the newest build expired")
	}
	if _, ok := got["sha256:b"]; ok {
		t.Error("the second newest build expired")
	}
	if !got["sha256:c"].Equal(now) {
		t.Errorf("the third build: %v", got["sha256:c"])
	}
	// Days since pushed, by pattern.
	got = Expiries(`{"rules":[{"rulePriority":1,"selection":{"tagStatus":"tagged","tagPatternList":["*"],"countType":"sinceImagePushed","countUnit":"days","countNumber":30}}]}`, images, now)
	if !got["sha256:b"].Equal(day(8).AddDate(0, 0, 30)) || len(got) != 4 {
		t.Errorf("since pushed: %v", got)
	}
	if Expiries("not json", images, now) != nil || Expiries("", images, now) != nil {
		t.Error("an unreadable policy expires something")
	}
}
