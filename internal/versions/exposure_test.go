// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// Within an urgency level an exposed update comes first, even before production; a more urgent one still leads.
func TestWithExposure(t *testing.T) {
	prod, stg := store.Environment{ID: "prod", Position: 2}, store.Environment{ID: "stg", Position: 1}
	us := []Update{
		{Service: store.Service{ID: "db", Name: "db"}, Environment: prod, Urgency: UrgencySecurity},
		{Service: store.Service{ID: "api", Name: "api"}, Environment: prod, Urgency: UrgencyMajor},
		{Service: store.Service{ID: "web", Name: "web"}, Environment: stg, Urgency: UrgencyMajor},
	}
	active := []store.Instance{
		{ServiceID: "web", EnvironmentID: "stg", IsMain: true, Exposed: []string{"ingress staging.example.com"}},
		{ServiceID: "web", EnvironmentID: "stg", IsMain: true, Exposed: []string{"ingress staging.example.com", "NodePort service shop/web"}},
		{ServiceID: "api", EnvironmentID: "prod", IsMain: false, Exposed: []string{"port 9090/tcp published"}}, // a sidecar
	}
	got := WithExposure(us, active)
	if got[0].Service.ID != "db" || got[1].Service.ID != "web" || got[2].Service.ID != "api" {
		t.Fatalf("order %s %s %s", got[0].Service.ID, got[1].Service.ID, got[2].Service.ID)
	}
	if len(got[1].Exposed) != 2 || len(got[2].Exposed) != 0 {
		t.Errorf("exposed web %v api %v", got[1].Exposed, got[2].Exposed)
	}
	f := VulnFinding{Affected: []Affected{{Service: "api"}, {Service: "web", Exposed: []string{"public IP"}}}}
	if !f.IsExposed() || (VulnFinding{Affected: []Affected{{Service: "api"}}}).IsExposed() {
		t.Error("IsExposed")
	}
}
