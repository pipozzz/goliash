// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A service's images: the running one against its budget, with base and evidence, the history, and what the last
// deploy changed in the layers.
func TestServiceImages(t *testing.T) {
	e := newPublicEnv(t)
	ctx := context.Background()
	web, err := e.st.GetServiceByName(ctx, e.sc, "web")
	if err != nil {
		t.Fatal(err)
	}
	web.VersionPolicy = json.RawMessage(`{"max_size_mb":100}`)
	if err := e.st.UpdateService(ctx, web); err != nil {
		t.Fatal(err)
	}
	var prod store.Instance
	active, _ := e.st.ListActiveInstances(ctx, e.sc)
	for _, in := range active {
		if in.Tag == "1.27.2" {
			prod = in
		}
	}
	// prod moves from 1.27.2 to 1.28.0, which adds Chromium.
	deploy := func(remove string, tag, digest string, at time.Time) string {
		in := prod
		in.ID, in.Tag, in.Image, in.Digest = "", tag, "nginx:"+tag, digest
		snap := store.NewID()
		_, _ = e.st.InsertSnapshot(ctx, store.Snapshot{ID: snap, Scope: e.sc, TargetID: in.TargetID, CollectedAt: at, Complete: true, Payload: json.RawMessage(`{}`)})
		if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: e.sc, SnapshotID: snap, TargetID: in.TargetID, At: at, Upsert: []store.Instance{in}, Remove: []string{remove}}); err != nil {
			t.Fatal(err)
		}
		active, _ := e.st.ListActiveInstances(ctx, e.sc)
		for _, a := range active {
			if a.Digest == digest {
				return a.ID
			}
		}
		t.Fatalf("%s not running", digest)
		return ""
	}
	old := deploy(prod.ID, "1.27.2", "sha256:old", time.Now().Add(-72*time.Hour))
	deploy(old, "1.28.0", "sha256:new", time.Now().Add(-24*time.Hour))
	base := store.ImageLayer{Digest: "sha256:base", Size: 30_000_000, Command: "ADD rootfs.tar.xz /"}
	now := time.Now()
	_ = e.st.SetImageEvidence(ctx, e.sc, store.ImageEvidence{
		Repo: "docker.io/library/nginx", Digest: "sha256:old", CheckedAt: now, Size: 50_000_000,
		Layers: []store.ImageLayer{base, {Digest: "sha256:app1", Size: 20_000_000, Command: "COPY . /app"}},
	})
	_ = e.st.SetImageEvidence(ctx, e.sc, store.ImageEvidence{
		Repo: "docker.io/library/nginx", Digest: "sha256:new", CheckedAt: now, Signed: true, Size: 180_000_000,
		BaseImage: "docker.io/library/debian:12", BaseStatus: "outdated", BaseSince: now.AddDate(0, 0, -20),
		Layers: []store.ImageLayer{base, {Digest: "sha256:chrome", Size: 130_000_000, Command: "RUN apt-get install -y chromium"}, {Digest: "sha256:app2", Size: 20_000_000, Command: "COPY . /app"}},
	})

	code, body := e.call(nil, "GET", "/api/v1/services/web/images", e.token, "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var got struct {
		BudgetBytes int64      `json:"budget_bytes"`
		Images      []apiImage `json:"images"`
		History     []apiImagePeriod
		LastChange  *apiSizeChange `json:"last_change"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.BudgetBytes != 100_000_000 {
		t.Errorf("budget %d", got.BudgetBytes)
	}
	var p *apiImage
	for i := range got.Images {
		if got.Images[i].Environment == "prod" {
			p = &got.Images[i]
		}
	}
	if p == nil || p.Version != "1.28.0" || p.SizeBytes != 180_000_000 || !p.OverBudget || p.Layers != 3 ||
		p.BaseStatus != "outdated" || p.BaseBehindDays != 20 || p.Evidence == nil || !p.Evidence.Signed || p.Evidence.SBOM {
		t.Fatalf("prod image %+v\n%s", p, body)
	}
	if len(got.History) < 2 {
		t.Errorf("history %s", body)
	}
	c := got.LastChange
	if c == nil || c.Environment != "prod" || c.From != "1.27.2" || c.To != "1.28.0" || c.BeforeBytes != 50_000_000 ||
		c.AfterBytes != 180_000_000 || len(c.Added) != 2 || c.Added[0].Command != "RUN apt-get install -y chromium" || len(c.Removed) != 1 {
		t.Fatalf("last change %+v\n%s", c, body)
	}
}
