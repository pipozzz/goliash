// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestExtendedSupportCost(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		repo, eol string
		vcpus     int
		ok        bool
		monthly   float64
		text      string
	}{
		{"rds.amazonaws.com/postgres", "2026-02-28", 4, true, 292, "$292/month from 2026-02-28 (4 vCPU × $0.10 an hour)"},
		{"rds.amazonaws.com/mysql", "2024-02-29", 4, true, 584, "$584/month from 2024-02-29 (4 vCPU × $0.20 an hour)"}, // year 3
		{"rds.amazonaws.com/aurora-postgresql", "2027-02-28", 0, true, 0, "charged from 2027-02-28 (per vCPU and hour)"},
		{"eks.amazonaws.com/kubernetes", "2026-12-02", 0, true, 365, "$365/month from 2026-12-02 ($0.50 an hour more for the cluster)"},
		{"elasticache.amazonaws.com/redis", "2026-01-31", 0, true, 0, "charged from 2026-01-31 (a share of the node price)"},
		{"rds.amazonaws.com/mariadb", "2026-01-31", 4, false, 0, ""},
		{"docker.io/library/postgres", "2026-01-31", 4, false, 0, ""},
		{"rds.amazonaws.com/postgres", "", 4, false, 0, ""},
	} {
		es, ok := ExtendedSupportCost(c.repo, c.eol, c.vcpus, now)
		if ok != c.ok || es.Monthly != c.monthly || (ok && es.String() != c.text) {
			t.Errorf("%s %s: %v %v %q", c.repo, c.eol, ok, es.Monthly, es.String())
		}
	}
	if Dollars(1168.4) != "$1,168" || Dollars(36500) != "$36,500" || Dollars(7) != "$7" {
		t.Error(Dollars(1168.4), Dollars(36500))
	}
}

// The vCPUs of a service's databases in an environment add up, and only the running version's.
func TestExtendedSupportOnUpdates(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	us := []Update{
		{Service: store.Service{ID: "db"}, Environment: store.Environment{ID: "prod"}, Running: "13.15", EOL: "2026-02-28", EOLPassed: true},
		{Service: store.Service{ID: "web"}, Environment: store.Environment{ID: "prod"}, Running: "1.27", EOL: "2026-02-28"},
	}
	active := []store.Instance{
		{ServiceID: "db", EnvironmentID: "prod", IsMain: true, Image: "rds.amazonaws.com/postgres:13.15", VCPUs: 4},
		{ServiceID: "db", EnvironmentID: "prod", IsMain: true, Image: "rds.amazonaws.com/postgres:13.15", VCPUs: 8, TargetID: "other-region"},
		{ServiceID: "db", EnvironmentID: "prod", IsMain: true, Image: "rds.amazonaws.com/postgres:16.4", VCPUs: 2},
		{ServiceID: "web", EnvironmentID: "prod", IsMain: true, Image: "nginx:1.27"},
	}
	out := withExposureAt(us, active, now)
	for _, u := range out {
		switch u.Service.ID {
		case "db":
			if u.ExtendedSupport == nil || int(u.ExtendedSupport.Monthly+0.5) != 876 {
				t.Fatalf("db: %+v", u.ExtendedSupport)
			}
		case "web":
			if u.ExtendedSupport != nil {
				t.Fatalf("an image has no Extended Support: %+v", u.ExtendedSupport)
			}
		}
	}
}
