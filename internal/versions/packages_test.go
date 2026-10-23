// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// pkgTags is a registry whose image has no SBOM but an Ubuntu package database, with nginx and php from apt.
type pkgTags struct {
	*fakeTags
	reads atomic.Int32
}

func (p *pkgTags) ImageSBOM(context.Context, string, string, registry.Credentials) (registry.SBOM, error) {
	return registry.SBOM{}, registry.ErrNotFound
}

func (p *pkgTags) ImagePackages(context.Context, string, string, registry.Credentials) (registry.SBOM, error) {
	p.reads.Add(1)
	return registry.SBOM{Purls: []string{
		"pkg:deb/ubuntu/nginx@1.18.0-6ubuntu14.3?arch=amd64&os_version=22.04",
		"pkg:deb/ubuntu/php8.1-fpm@8.1.2-1ubuntu2.14?arch=amd64&os_version=22.04&upstream=php8.1",
	}}, nil
}

// An image built from ubuntu with nginx and php installed by apt, without an SBOM: Goliash reads its package
// database, OSV answers for Ubuntu's packages, and the vulnerability shows like any other.
func TestPackagesWithoutSBOM(t *testing.T) {
	osv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/querybatch":
			var req struct {
				Queries []osvQuery `json:"queries"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			var results []map[string]any
			for _, q := range req.Queries {
				if q.Package.Ecosystem == "Ubuntu:22.04" && q.Package.Name == "nginx" {
					results = append(results, map[string]any{"vulns": []map[string]string{{"id": "UBUNTU-CVE-2023-44487"}}})
				} else {
					results = append(results, map[string]any{})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		case strings.HasPrefix(r.URL.Path, "/vulns/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/vulns/"), "summary": "HTTP/2 rapid reset", "upstream": []string{"CVE-2023-44487"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer osv.Close()

	l := newLab(t)
	ctx := context.Background()
	reg := &pkgTags{fakeTags: l.tags}
	l.checker = NewChecker(l.st, reg, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	l.checker.SetOSV(NewOSV(osv.URL))
	tgt := l.targets["prod-a"]
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "w", WorkloadName: "web", ContainerName: "web",
		Image: "nginx:1.25.3", Tag: "1.25.3", Digest: "sha256:abc", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	// Before its evidence is read, nothing: whether it has an SBOM is not known yet.
	if err := l.checker.CheckVulnerabilities(ctx, l.sc); err != nil || reg.reads.Load() != 0 {
		t.Fatalf("read before the evidence: %d %v", reg.reads.Load(), err)
	}
	if r, _ := LoadVulnReport(ctx, l.st, l.sc, ""); len(r.Blind) != 1 || !strings.Contains(r.Blind[0].Why, "not read yet") {
		t.Fatalf("blind %+v", r.Blind)
	}
	if err := l.st.SetImageEvidence(ctx, l.sc, store.ImageEvidence{Repo: "docker.io/library/nginx", Digest: "sha256:abc"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := l.checker.CheckVulnerabilities(ctx, l.sc); err != nil {
			t.Fatal(err)
		}
	}
	if reg.reads.Load() != 1 {
		t.Fatalf("package database read %d times", reg.reads.Load())
	}
	r, err := LoadVulnReport(ctx, l.st, l.sc, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.WithSBOM != 1 || r.FromPackages != 1 || len(r.Blind) != 0 || len(r.Findings) != 1 || r.Findings[0].Key != "CVE-2023-44487" ||
		r.Findings[0].Affected[0].Package != "nginx 1.18.0-6ubuntu14.3" {
		t.Fatalf("report %+v", r)
	}
}

func TestReadFailure(t *testing.T) {
	for err, want := range map[string]string{
		`registry answered 404: {"errors":[{"code":"MANIFEST_UNKNOWN"}]}`: "no longer has this image",
		"registry denied access: token endpoint answered 403":             "refused the server",
		"the image is larger than 1024 MB to read for its packages":       "too large",
	} {
		if got := readFailure(err); !strings.Contains(got, want) {
			t.Errorf("%s: %q", err, got)
		}
	}
}
