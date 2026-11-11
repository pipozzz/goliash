// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// DistroStatus is what a distribution's security tracker says of a CVE in one of its source packages.
type DistroStatus struct {
	Ecosystem string // Debian:12
	Package   string // the source package
	CVE       string
	Status    string // not_affected, unimportant or undetermined
}

// DistroStatusKey is how DistroStatuses keys a status.
func DistroStatusKey(ecosystem, pkg, cve string) string { return ecosystem + "|" + pkg + "|" + cve }

// SetDistroStatuses replaces what is known from a distribution's tracker (distro: "Debian", every release of it).
func (s *Store) SetDistroStatuses(ctx context.Context, distro string, list []DistroStatus) error {
	now := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `DELETE FROM distro_statuses WHERE ecosystem LIKE ?`, distro+":%"); err != nil {
			return err
		}
		const batch = 200 // rows an INSERT carries: thousands are kept, one at a time is slow
		for i := 0; i < len(list); i += batch {
			part := list[i:min(i+batch, len(list))]
			args := make([]any, 0, 5*len(part))
			for _, d := range part {
				args = append(args, d.Ecosystem, d.Package, d.CVE, d.Status, now)
			}
			q := `INSERT INTO distro_statuses (ecosystem, package, cve, status, fetched_at) VALUES ` +
				strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?), ", len(part)), ", ") + ` ON CONFLICT (ecosystem, package, cve) DO NOTHING`
			if _, err := s.exec(ctx, tx, q, args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// DistroStatuses returns every status by DistroStatusKey, and when distro's were last fetched (zero when never).
func (s *Store) DistroStatuses(ctx context.Context, distro string) (map[string]string, time.Time, error) {
	rows, err := s.query(ctx, s.db, `SELECT ecosystem, package, cve, status, fetched_at FROM distro_statuses`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	var fetched time.Time
	for rows.Next() {
		var d DistroStatus
		var at time.Time
		if err := rows.Scan(&d.Ecosystem, &d.Package, &d.CVE, &d.Status, &at); err != nil {
			return nil, time.Time{}, err
		}
		out[DistroStatusKey(d.Ecosystem, d.Package, d.CVE)] = d.Status
		if strings.HasPrefix(d.Ecosystem, distro+":") && at.After(fetched) {
			fetched = at.UTC()
		}
	}
	return out, fetched, rows.Err()
}
