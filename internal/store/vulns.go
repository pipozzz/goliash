// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ImageSBOM is the package list of a running image's SBOM attestation.
type ImageSBOM struct {
	Repo, Digest string
	Purls        []string
	Licenses     map[string]string // purl -> SPDX license expression, for the packages whose license the SBOM names
	// LicensesRead is false for an SBOM read before licenses were kept.
	LicensesRead bool
	Error        string
	FetchedAt    time.Time
	// Source is where the packages came from: "sbom", the image's SBOM attestation, or "packages", its operating
	// system package database read by Goliash for an image without one.
	Source string
}

// SetImageSBOM records what an image's SBOM lists, or why it could not be read.
func (s *Store) SetImageSBOM(ctx context.Context, sc Scope, b ImageSBOM) error {
	purls := jsonList(b.Purls)
	licenses := []byte("{}")
	if len(b.Licenses) > 0 {
		var err error
		if licenses, err = json.Marshal(b.Licenses); err != nil {
			return err
		}
	}
	if b.Source == "" {
		b.Source = "sbom"
	}
	_, err := s.exec(ctx, s.db, `INSERT INTO image_sboms (org_id, workspace_id, repo, digest, purls, licenses, error, fetched_at, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, repo, digest) DO UPDATE SET purls = excluded.purls, licenses = excluded.licenses,
			error = excluded.error, fetched_at = excluded.fetched_at, source = excluded.source`,
		sc.OrgID, sc.WorkspaceID, b.Repo, b.Digest, purls, string(licenses), b.Error, s.now(), b.Source)
	return err
}

// ListImageSBOMs returns the workspace's SBOMs keyed by repo + "@" + digest.
func (s *Store) listImageSBOMs(ctx context.Context, sc Scope) (map[string]ImageSBOM, error) {
	rows, err := s.query(ctx, s.db, `SELECT repo, digest, purls, licenses, error, fetched_at, source FROM image_sboms
		WHERE org_id = ? AND workspace_id = ?`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]ImageSBOM{}
	for rows.Next() {
		var b ImageSBOM
		var purls string
		var licenses sql.NullString
		if err := rows.Scan(&b.Repo, &b.Digest, &purls, &licenses, &b.Error, &b.FetchedAt, &b.Source); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(purls), &b.Purls)
		b.LicensesRead = licenses.Valid
		_ = json.Unmarshal([]byte(licenses.String), &b.Licenses)
		b.FetchedAt = b.FetchedAt.UTC()
		out[b.Repo+"@"+b.Digest] = b
	}
	return out, rows.Err()
}

// PackageVulns are the vulnerability IDs OSV knows for a package URL, as of CheckedAt.
type PackageVulns struct {
	Purl      string
	Vulns     []string
	CheckedAt time.Time
}

// SetPackageVulns records OSV's answer for package URLs.
func (s *Store) SetPackageVulns(ctx context.Context, answers map[string][]string) error {
	now := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for purl, ids := range answers {
			if _, err := s.exec(ctx, tx, `INSERT INTO osv_packages (purl, vulns, checked_at) VALUES (?, ?, ?)
				ON CONFLICT (purl) DO UPDATE SET vulns = excluded.vulns, checked_at = excluded.checked_at`,
				purl, jsonList(ids), now); err != nil {
				return err
			}
		}
		return nil
	})
}

// PackageVulnsOf returns what is known of the given package URLs.
func (s *Store) PackageVulnsOf(ctx context.Context, purls []string) (map[string]PackageVulns, error) {
	out := map[string]PackageVulns{}
	for start := 0; start < len(purls); start += 500 {
		chunk := purls[start:min(start+500, len(purls))]
		args := make([]any, len(chunk))
		marks := make([]byte, 0, 2*len(chunk))
		for i, p := range chunk {
			args[i] = p
			if i > 0 {
				marks = append(marks, ',')
			}
			marks = append(marks, '?')
		}
		rows, err := s.query(ctx, s.db, `SELECT purl, vulns, checked_at FROM osv_packages WHERE purl IN (`+string(marks)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p PackageVulns
			var ids string
			if err := rows.Scan(&p.Purl, &ids, &p.CheckedAt); err != nil {
				_ = rows.Close()
				return nil, err
			}
			_ = json.Unmarshal([]byte(ids), &p.Vulns)
			p.CheckedAt = p.CheckedAt.UTC()
			out[p.Purl] = p
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Vuln is what Goliash keeps of a vulnerability record.
type Vuln struct {
	ID        string
	Aliases   []string
	Summary   string
	Severity  string
	Fixes     map[string][]string // "ecosystem|package" -> versions that fix it; nil until read with them
	FetchedAt time.Time
}

// SetVuln records a vulnerability's details.
func (s *Store) SetVuln(ctx context.Context, v Vuln) error {
	fixes := []byte("{}")
	if len(v.Fixes) > 0 {
		var err error
		if fixes, err = json.Marshal(v.Fixes); err != nil {
			return err
		}
	}
	_, err := s.exec(ctx, s.db, `INSERT INTO osv_vulns (id, aliases, summary, severity, fixes, fetched_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET aliases = excluded.aliases, summary = excluded.summary, severity = excluded.severity,
			fixes = excluded.fixes, fetched_at = excluded.fetched_at`, v.ID, jsonList(v.Aliases), v.Summary, v.Severity, string(fixes), s.now())
	return err
}

// ListVulns returns every vulnerability record kept, by ID.
func (s *Store) listVulns(ctx context.Context) (map[string]Vuln, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, aliases, summary, severity, fixes, fetched_at FROM osv_vulns`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Vuln{}
	for rows.Next() {
		var v Vuln
		var aliases, fixes string
		if err := rows.Scan(&v.ID, &aliases, &v.Summary, &v.Severity, &fixes, &v.FetchedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(aliases), &v.Aliases)
		if fixes != "" {
			v.Fixes = map[string][]string{}
			_ = json.Unmarshal([]byte(fixes), &v.Fixes)
		}
		out[v.ID] = v
	}
	return out, rows.Err()
}

func jsonList(v []string) string {
	if len(v) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Exploited is a vulnerability in CISA's catalog of known exploited vulnerabilities (KEV).
type Exploited struct {
	CVE, VendorProduct, Name string
	DateAdded, DueDate       string // YYYY-MM-DD
	Ransomware               bool   // known to be used in ransomware campaigns
}

// SetKnownExploited replaces the catalog of known exploited vulnerabilities.
func (s *Store) SetKnownExploited(ctx context.Context, list []Exploited) error {
	now := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `DELETE FROM known_exploited`); err != nil {
			return err
		}
		for _, e := range list {
			if _, err := s.exec(ctx, tx, `INSERT INTO known_exploited (cve, vendor_product, name, date_added, due_date, ransomware, fetched_at)
				VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (cve) DO NOTHING`,
				e.CVE, e.VendorProduct, e.Name, e.DateAdded, e.DueDate, e.Ransomware, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// KnownExploited returns the catalog by CVE, and when it was last fetched (zero when never).
func (s *Store) knownExploited(ctx context.Context) (map[string]Exploited, time.Time, error) {
	rows, err := s.query(ctx, s.db, `SELECT cve, vendor_product, name, date_added, due_date, ransomware, fetched_at FROM known_exploited`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Exploited{}
	var fetched time.Time
	for rows.Next() {
		var e Exploited
		var at time.Time
		if err := rows.Scan(&e.CVE, &e.VendorProduct, &e.Name, &e.DateAdded, &e.DueDate, &e.Ransomware, &at); err != nil {
			return nil, time.Time{}, err
		}
		out[e.CVE] = e
		if at.After(fetched) {
			fetched = at.UTC()
		}
	}
	return out, fetched, rows.Err()
}

// VulnAlert is a vulnerability announced for a service in an environment.
type VulnAlert struct {
	CVE, ServiceID, EnvironmentID string
}

// VulnAlerts returns what was announced in the workspace, and whether its first look was recorded.
func (s *Store) VulnAlerts(ctx context.Context, sc Scope) (map[VulnAlert]bool, bool, error) {
	rows, err := s.query(ctx, s.db, `SELECT cve, service_id, environment_id FROM vulnerability_alerts
		WHERE org_id = ? AND workspace_id = ?`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	out := map[VulnAlert]bool{}
	baseline := false
	for rows.Next() {
		var a VulnAlert
		if err := rows.Scan(&a.CVE, &a.ServiceID, &a.EnvironmentID); err != nil {
			return nil, false, err
		}
		if a.CVE == "" {
			baseline = true
			continue
		}
		out[a] = true
	}
	return out, baseline, rows.Err()
}

// RecordVulnAlerts records announced vulnerabilities; with baseline, also the workspace's first look.
func (s *Store) RecordVulnAlerts(ctx context.Context, sc Scope, alerts []VulnAlert, baseline bool) error {
	if baseline {
		alerts = append(alerts, VulnAlert{})
	}
	now := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, a := range alerts {
			if _, err := s.exec(ctx, tx, `INSERT INTO vulnerability_alerts (org_id, workspace_id, cve, service_id, environment_id, alerted_at)
				VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (workspace_id, cve, service_id, environment_id) DO NOTHING`,
				sc.OrgID, sc.WorkspaceID, a.CVE, a.ServiceID, a.EnvironmentID, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// OSVCheckedAt is when OSV last answered about a package, zero before the first answer.
func (s *Store) OSVCheckedAt(ctx context.Context) (time.Time, error) {
	var at sql.NullTime
	err := s.queryRow(ctx, s.db, `SELECT checked_at FROM osv_packages ORDER BY checked_at DESC LIMIT 1`).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	return timeOrZero(at), err
}
