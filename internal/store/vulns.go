// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// ImageSBOM is the package list of a running image's SBOM attestation.
type ImageSBOM struct {
	Repo, Digest string
	Purls        []string
	Error        string
	FetchedAt    time.Time
}

// SetImageSBOM records what an image's SBOM lists, or why it could not be read.
func (s *Store) SetImageSBOM(ctx context.Context, sc Scope, b ImageSBOM) error {
	purls := jsonList(b.Purls)
	_, err := s.exec(ctx, s.db, `INSERT INTO image_sboms (org_id, workspace_id, repo, digest, purls, error, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, repo, digest) DO UPDATE SET purls = excluded.purls, error = excluded.error, fetched_at = excluded.fetched_at`,
		sc.OrgID, sc.WorkspaceID, b.Repo, b.Digest, purls, b.Error, s.now())
	return err
}

// ListImageSBOMs returns the workspace's SBOMs keyed by repo + "@" + digest.
func (s *Store) ListImageSBOMs(ctx context.Context, sc Scope) (map[string]ImageSBOM, error) {
	rows, err := s.query(ctx, s.db, `SELECT repo, digest, purls, error, fetched_at FROM image_sboms
		WHERE org_id = ? AND workspace_id = ?`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]ImageSBOM{}
	for rows.Next() {
		var b ImageSBOM
		var purls string
		if err := rows.Scan(&b.Repo, &b.Digest, &purls, &b.Error, &b.FetchedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(purls), &b.Purls)
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
	FetchedAt time.Time
}

// SetVuln records a vulnerability's details.
func (s *Store) SetVuln(ctx context.Context, v Vuln) error {
	_, err := s.exec(ctx, s.db, `INSERT INTO osv_vulns (id, aliases, summary, severity, fetched_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET aliases = excluded.aliases, summary = excluded.summary, severity = excluded.severity,
			fetched_at = excluded.fetched_at`, v.ID, jsonList(v.Aliases), v.Summary, v.Severity, s.now())
	return err
}

// ListVulns returns every vulnerability record kept, by ID.
func (s *Store) ListVulns(ctx context.Context) (map[string]Vuln, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, aliases, summary, severity, fetched_at FROM osv_vulns`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Vuln{}
	for rows.Next() {
		var v Vuln
		var aliases string
		if err := rows.Scan(&v.ID, &aliases, &v.Summary, &v.Severity, &v.FetchedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(aliases), &v.Aliases)
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
