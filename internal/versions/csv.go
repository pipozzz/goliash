// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"encoding/csv"
	"io"
	"strconv"
	"strings"
	"time"
)

// csvCell keeps a value from being read as a formula by spreadsheets: package names, licenses and summaries come
// from SBOMs and advisories, which anyone can write.
func csvCell(s string) string {
	if s != "" && strings.ContainsAny(s[:1], "=+-@\t\r") {
		return "'" + s
	}
	return s
}

func writeCSV(w io.Writer, header []string, rows [][]string) error {
	cw := csv.NewWriter(w)
	_ = cw.Write(header)
	for _, r := range rows {
		for i := range r {
			r[i] = csvCell(r[i])
		}
		_ = cw.Write(r)
	}
	cw.Flush()
	return cw.Error()
}

// WriteLicensesCSV writes packages and their licenses, one row per package and where it runs, for a license review.
func WriteLicensesCSV(w io.Writer, pkgs []LicensedPackage) error {
	var rows [][]string
	for _, p := range pkgs {
		where := p.Where
		if len(where) == 0 {
			where = []LicenseUse{{}}
		}
		for _, u := range where {
			rows = append(rows, []string{
				string(p.Class), p.Name, p.Version, p.License, strconv.FormatBool(p.OS), u.Service, u.Environment, p.Purl,
			})
		}
	}
	return writeCSV(w, []string{"class", "package", "version", "license", "os_package", "service", "environment", "purl"}, rows)
}

// WriteVulnsCSV writes known vulnerabilities, one row per vulnerability and affected image, for an audit or a
// ticket system: CISA's exploited flag and due date, the fix, and since when it runs there.
func WriteVulnsCSV(w io.Writer, findings []VulnFinding, now time.Time) error {
	var rows [][]string
	for _, f := range findings {
		exploited, ransomware, due := "false", "false", ""
		if f.Exploited != nil {
			exploited, ransomware, due = "true", strconv.FormatBool(f.Exploited.Ransomware), f.Exploited.DueDate
		}
		for _, a := range f.Affected {
			since := ""
			if !a.Since.IsZero() {
				since = a.Since.UTC().Format(time.DateOnly)
			}
			rows = append(rows, []string{
				f.Key, strings.Join(f.Aliases, " "), strings.ToLower(f.Severity), exploited, ransomware, due,
				strconv.FormatBool(f.PastDue(now)), a.Service, a.Env, a.Image, a.Package, a.Fix, since, f.Summary,
			})
		}
	}
	return writeCSV(w, []string{
		"id", "aliases", "severity", "exploited", "ransomware", "cisa_due_date", "past_due",
		"service", "environment", "image", "package", "fix", "running_since", "summary",
	}, rows)
}
