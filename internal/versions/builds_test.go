// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestNewestBuildOf(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	times := []store.TagTime{{Tag: "latest", PushedAt: day(9)}, {Tag: "f00d1e5", PushedAt: day(9)}, {Tag: "prod", PushedAt: day(8)}, {Tag: "bca73851", PushedAt: day(3)}}
	b, ok := NewestBuildOf(times, "bca73851")
	if !ok || b.Tag != "f00d1e5" || !b.Newer || !b.RunningPushedAt.Equal(day(3)) {
		t.Fatalf("%+v %v", b, ok)
	}
	if b, _ := NewestBuildOf(times, "f00d1e5"); b.Newer {
		t.Fatalf("runs the newest: %+v", b)
	}
	if _, ok := NewestBuildOf([]store.TagTime{{Tag: "latest", PushedAt: day(9)}}, "latest"); ok {
		t.Fatal("only a moving tag: no build")
	}
}

// A running image its lifecycle policy deletes is a warning, by digest or else by tag.
func TestExpiringImages(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	repo := "240220093055.dkr.ecr.us-west-2.amazonaws.com/chat"
	active := []store.Instance{
		{EnvironmentID: "p", TargetID: "t", WorkloadName: "chat", ContainerName: "app", Image: repo + ":bca73851", Digest: "sha256:b"},
		{EnvironmentID: "p", TargetID: "t", WorkloadName: "web", ContainerName: "app", Image: repo + ":f00d1e5", Digest: "sha256:a"},
	}
	expiring := []store.TagTime{
		{Repo: repo, Tag: "bca73851", Digest: "sha256:b", ExpiresAt: now.AddDate(0, 0, 3)},
		{Repo: repo, Tag: "old", Digest: "sha256:z", ExpiresAt: now},
	}
	got := ExpiringImages(active, expiring, map[string]string{"p": "production"}, map[string]string{"t": "fargate"}, now)
	if len(got) != 1 || got[0].Kind != "expiring" || !strings.Contains(got[0].Detail, "on 2026-11-14") || got[0].Where[0] != "production / fargate / chat / app" {
		t.Fatalf("%+v", got)
	}
}
