// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"time"
)

// UpstreamRange is a range of a product's versions NVD says a CVE affects. Empty bounds are open.
type UpstreamRange struct {
	Product   string `json:"product"` // vendor:product, f5:nginx
	StartIncl string `json:"start_incl,omitempty"`
	StartExcl string `json:"start_excl,omitempty"`
	EndIncl   string `json:"end_incl,omitempty"`
	EndExcl   string `json:"end_excl,omitempty"` // the first fixed version
	Exact     string `json:"exact,omitempty"`    // one version, when NVD names it instead of a range
}

// UpstreamCVE is what NVD said of a CVE's upstream products, and when.
type UpstreamCVE struct {
	CVE       string
	Ranges    []UpstreamRange
	CheckedAt time.Time
}

// SetUpstreamCVE records NVD's ranges for a CVE.
func (s *Store) SetUpstreamCVE(ctx context.Context, cve string, ranges []UpstreamRange) error {
	if ranges == nil {
		ranges = []UpstreamRange{}
	}
	b, err := json.Marshal(ranges)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, s.db, `INSERT INTO upstream_cves (cve, ranges, checked_at) VALUES (?, ?, ?)
		ON CONFLICT (cve) DO UPDATE SET ranges = excluded.ranges, checked_at = excluded.checked_at`, cve, string(b), s.now())
	return err
}

// ListUpstreamCVEs returns every CVE asked, by CVE.
func (s *Store) ListUpstreamCVEs(ctx context.Context) (map[string]UpstreamCVE, error) {
	rows, err := s.query(ctx, s.db, `SELECT cve, ranges, checked_at FROM upstream_cves`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]UpstreamCVE{}
	for rows.Next() {
		var u UpstreamCVE
		var raw string
		if err := rows.Scan(&u.CVE, &raw, &u.CheckedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &u.Ranges)
		u.CheckedAt = u.CheckedAt.UTC()
		out[u.CVE] = u
	}
	return out, rows.Err()
}
