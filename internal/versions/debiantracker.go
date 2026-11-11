// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// OSV's Debian advisories come from Debian's security tracker, sometimes days behind it. Two of its answers matter
// most and get lost: that a release never had the vulnerable code (OSV may still list it as affected, with no fix),
// and that Debian has not assessed a CVE yet (OSV lists it as affected). The tracker's own data corrects both.

// DefaultDebianTrackerURL is the tracker's data, every source package and CVE (about 15 MB compressed).
const DefaultDebianTrackerURL = "https://security-tracker.debian.org/tracker/data/json"

// debianTrackerRefresh is how often it is read again.
const debianTrackerRefresh = 24 * time.Hour

// debianReleases are the tracker's release names, as OSV's ecosystems name them.
var debianReleases = map[string]string{
	"bullseye": "Debian:11", "bookworm": "Debian:12", "trixie": "Debian:13", "forky": "Debian:14", "duke": "Debian:15",
}

// Distro statuses.
const (
	DistroNotAffected  = "not_affected"
	DistroUndetermined = "undetermined"
	// DistroUnimportant is an open issue Debian rates unimportant for a release: no security update is planned, as
	// its build is not affected in practice (an OpenSSH flaw only musl builds have, say).
	DistroUnimportant = "unimportant"
)

// SetDebianTracker sets where the tracker's data is read from; empty means Debian's.
func (c *Checker) SetDebianTracker(url string) {
	if url == "" {
		url = DefaultDebianTrackerURL
	}
	c.debianTracker = url
}

// refreshDebianTracker reads the tracker once a day, when a running image has Debian packages.
func (c *Checker) refreshDebianTracker(ctx context.Context, purls []string) error {
	if c.debianTracker == "" || !hasDebian(purls) {
		return nil
	}
	_, fetched, err := c.store.DistroStatuses(ctx, "Debian")
	if err != nil || (!fetched.IsZero() && c.now().Sub(fetched) < debianTrackerRefresh) || c.now().Sub(c.debianTried) < time.Hour {
		return err
	}
	c.debianTried = c.now()
	list, err := fetchDebianTracker(ctx, c.debianTracker)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return errors.New("the Debian security tracker's data is empty")
	}
	return c.store.SetDistroStatuses(ctx, "Debian", list)
}

func hasDebian(purls []string) bool {
	for _, p := range purls {
		if strings.HasPrefix(p, "pkg:deb/debian/") {
			return true
		}
	}
	return false
}

// fetchDebianTracker reads the tracker's data a source package at a time, keeping what OSV may get wrong.
func fetchDebianTracker(ctx context.Context, url string) ([]store.DistroStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // the tracker's URL, set by the operator
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("debian security tracker: %s", resp.Status)
	}
	dec := json.NewDecoder(resp.Body)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("debian security tracker: not an object (%w)", err)
	}
	type release struct {
		Status       string `json:"status"`
		FixedVersion string `json:"fixed_version"`
		Urgency      string `json:"urgency"`
	}
	var out []store.DistroStatus
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		pkg, _ := t.(string)
		var cves map[string]struct {
			Releases map[string]release `json:"releases"`
		}
		if err := dec.Decode(&cves); err != nil {
			return nil, fmt.Errorf("debian security tracker: %s: %w", pkg, err)
		}
		for cve, e := range cves {
			if !strings.HasPrefix(cve, "CVE-") {
				continue
			}
			for name, r := range e.Releases {
				eco, ok := debianReleases[name]
				if !ok {
					continue
				}
				switch {
				case r.Status == "resolved" && r.FixedVersion == "0":
					out = append(out, store.DistroStatus{Ecosystem: eco, Package: pkg, CVE: cve, Status: DistroNotAffected})
				case r.Status == "open" && r.Urgency == "unimportant":
					out = append(out, store.DistroStatus{Ecosystem: eco, Package: pkg, CVE: cve, Status: DistroUnimportant})
				case r.Status == "undetermined":
					out = append(out, store.DistroStatus{Ecosystem: eco, Package: pkg, CVE: cve, Status: DistroUndetermined})
				}
			}
		}
	}
	return out, nil
}

// distroStatus is what the distribution's tracker says of a vulnerability (any of its CVEs) in purl's package.
func distroStatus(statuses map[string]string, purl string, cves []string) string {
	if len(statuses) == 0 {
		return ""
	}
	q, ok := osvPrimary(purl)
	if !ok || !strings.HasPrefix(q.Package.Ecosystem, "Debian:") {
		return ""
	}
	for _, c := range cves {
		if s := statuses[store.DistroStatusKey(q.Package.Ecosystem, q.Package.Name, c)]; s != "" {
			return s
		}
	}
	return ""
}
