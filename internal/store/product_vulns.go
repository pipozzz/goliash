// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"time"
)

// ProductVuln is a vulnerability NVD says affects a version of a product.
type ProductVuln struct {
	CVE      string  `json:"cve"`
	Summary  string  `json:"summary,omitempty"`
	Severity string  `json:"severity,omitempty"` // CRITICAL, HIGH, MEDIUM, LOW
	Score    float64 `json:"score,omitempty"`
	Fix      string  `json:"fix,omitempty"` // the first version of the product without it, when NVD says
}

// ProductVulns is what is known of one product version, and when it was asked.
type ProductVulns struct {
	Product, Version string
	Vulns            []ProductVuln
	CheckedAt        time.Time
}

// SetProductVulns records NVD's answer for a product version.
func (s *Store) SetProductVulns(ctx context.Context, product, version string, vulns []ProductVuln) error {
	if vulns == nil {
		vulns = []ProductVuln{}
	}
	b, err := json.Marshal(vulns)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, s.db, `INSERT INTO product_vulns (product, version, vulns, checked_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (product, version) DO UPDATE SET vulns = excluded.vulns, checked_at = excluded.checked_at`,
		product, version, string(b), s.now())
	return err
}

// ListProductVulns returns every product version asked, keyed by product + "@" + version.
func (s *Store) ListProductVulns(ctx context.Context) (map[string]ProductVulns, error) {
	rows, err := s.query(ctx, s.db, `SELECT product, version, vulns, checked_at FROM product_vulns`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]ProductVulns{}
	for rows.Next() {
		var p ProductVulns
		var raw string
		if err := rows.Scan(&p.Product, &p.Version, &raw, &p.CheckedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &p.Vulns)
		p.CheckedAt = p.CheckedAt.UTC()
		out[p.Product+"@"+p.Version] = p
	}
	return out, rows.Err()
}
