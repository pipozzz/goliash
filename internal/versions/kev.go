// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// KEV reads CISA's catalog of known exploited vulnerabilities, a public JSON file.
type KEV struct {
	HTTP *http.Client
	URL  string
}

// DefaultKEVURL is where CISA publishes the catalog.
const DefaultKEVURL = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"

// kevRefresh is how often the catalog is read again; CISA adds to it most working days.
const kevRefresh = 24 * time.Hour

// NewKEV returns a reader for url; empty means CISA's.
func NewKEV(url string) *KEV {
	if url == "" {
		url = DefaultKEVURL
	}
	return &KEV{HTTP: &http.Client{Timeout: 60 * time.Second}, URL: url}
}

// SetKEV gives the checker the catalog of known exploited vulnerabilities.
func (c *Checker) SetKEV(k *KEV) { c.kev = k }

// Fetch reads the catalog.
func (k *KEV) Fetch(ctx context.Context) ([]store.Exploited, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := k.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kev catalog answered %d", resp.StatusCode)
	}
	var doc struct {
		Vulnerabilities []struct {
			CVE        string `json:"cveID"`
			Vendor     string `json:"vendorProject"`
			Product    string `json:"product"`
			Name       string `json:"vulnerabilityName"`
			DateAdded  string `json:"dateAdded"`
			DueDate    string `json:"dueDate"`
			Ransomware string `json:"knownRansomwareCampaignUse"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("kev catalog: %w", err)
	}
	out := make([]store.Exploited, 0, len(doc.Vulnerabilities))
	for _, v := range doc.Vulnerabilities {
		if CVEOf(v.CVE) == "" {
			continue
		}
		out = append(out, store.Exploited{
			CVE: v.CVE, VendorProduct: strings.TrimSpace(v.Vendor + " " + v.Product), Name: strings.TrimSpace(v.Name),
			DateAdded: v.DateAdded, DueDate: v.DueDate, Ransomware: strings.EqualFold(v.Ransomware, "Known"),
		})
	}
	return out, nil
}

// refreshKEV reads the catalog again when it is a day old.
func (c *Checker) refreshKEV(ctx context.Context) error {
	if c.kev == nil {
		return nil
	}
	_, fetched, err := c.store.KnownExploited(ctx)
	if err != nil || (!fetched.IsZero() && c.now().Sub(fetched) < kevRefresh) {
		return err
	}
	list, err := c.kev.Fetch(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return nil // an empty catalog is a broken download, not good news
	}
	return c.store.SetKnownExploited(ctx, list)
}
