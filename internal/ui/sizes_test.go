// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A service's page charts its image's size per environment: the old image's size until the deploy, the new one's
// after.
func TestServiceSizes(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	svc, _ := e.st.EnsureService(ctx, sc, "api")
	deploy := func(digest string, at time.Time, remove []string) string {
		in := store.Instance{
			TargetID: e.tgt.ID, EnvironmentID: e.prod.ID, ServiceID: svc.ID, WorkloadID: "api", WorkloadName: "api",
			ContainerName: "api", Image: "ghcr.io/acme/api:" + digest[7:9], Tag: digest[7:9], Digest: digest, Running: 1, IsMain: true,
		}
		if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, SnapshotID: store.NewID(), TargetID: e.tgt.ID, At: at, Upsert: []store.Instance{in}, Remove: remove}); err != nil {
			t.Fatal(err)
		}
		active, _ := e.st.ListActiveInstances(ctx, sc)
		for _, a := range active {
			if a.Digest == digest {
				return a.ID
			}
		}
		t.Fatal("not running")
		return ""
	}
	now := time.Now()
	old := deploy("sha256:10", now.AddDate(0, 0, -10), nil)
	deploy("sha256:20", now.AddDate(0, 0, -3), []string{old})
	for digest, size := range map[string]int64{"sha256:10": 80_000_000, "sha256:20": 240_000_000} {
		if err := e.st.SetImageEvidence(ctx, sc, store.ImageEvidence{Repo: "ghcr.io/acme/api", Digest: digest, Size: size}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := e.ui.serviceSizes(ctx, sc, svc, now)
	if err != nil || len(c.Series) != 1 {
		t.Fatalf("chart %+v %v", c, err)
	}
	v := c.Series[0].Values
	if c.Series[0].Name != "prod" || v[0] != 80 || v[len(v)-1] != 240 || len(c.Labels) != len(v) || len(v) < 10 {
		t.Errorf("prod sizes %v", v)
	}
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/services/"+url.PathEscape("api"), nil)
	if !strings.Contains(page, `id="sizes"`) {
		t.Error("no size chart on the service page")
	}
}
