// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// BackstageServiceAnnotation names, on a Backstage component, the Goliash service it is when the names differ.
const BackstageServiceAnnotation = "goliash.dev/service"

// backstageTTL is how long the catalog is kept before it is read again.
const backstageTTL = time.Hour

// CatalogComponent is a Backstage component and the team that owns it.
type CatalogComponent struct {
	Name         string // metadata.name
	Service      string // the goliash.dev/service annotation
	KubernetesID string // the backstage.io/kubernetes-id annotation
	Team         string // spec.owner, a group: team-payments for group:default/team-payments; "" for a person
}

// Backstage reads the components of a Backstage software catalog, once an hour.
type Backstage struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	now     func() time.Time

	mu    sync.Mutex
	at    time.Time
	comps []CatalogComponent
	err   error
}

// NewBackstage returns a client for the catalog at base (https://backstage.example.com), with a static token
// (Backstage's external access) or none.
func NewBackstage(base, token string) *Backstage {
	return &Backstage{BaseURL: strings.TrimSuffix(base, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}, now: time.Now}
}

// Components lists the catalog's components, from the cache when it was read within the hour.
func (b *Backstage) Components(ctx context.Context) ([]CatalogComponent, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.at.IsZero() && b.now().Sub(b.at) < backstageTTL {
		return b.comps, b.err
	}
	comps, err := b.read(ctx)
	if err != nil && ctx.Err() != nil {
		return nil, err
	}
	b.comps, b.err, b.at = comps, err, b.now()
	return comps, err
}

func (b *Backstage) read(ctx context.Context) ([]CatalogComponent, error) {
	var out []CatalogComponent
	cursor := ""
	for page := 0; page < 100; page++ {
		q := url.Values{"filter": {"kind=component"}, "fields": {"metadata.name,metadata.annotations,spec.owner"}, "limit": {"500"}}
		if cursor != "" {
			q = url.Values{"cursor": {cursor}}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.BaseURL+"/api/catalog/entities/by-query?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "goliash")
		if b.Token != "" {
			req.Header.Set("Authorization", "Bearer "+b.Token)
		}
		resp, err := b.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		var body struct {
			Items []struct {
				Metadata struct {
					Name        string            `json:"name"`
					Annotations map[string]string `json:"annotations"`
				} `json:"metadata"`
				Spec struct {
					Owner string `json:"owner"`
				} `json:"spec"`
			} `json:"items"`
			PageInfo struct {
				NextCursor string `json:"nextCursor"`
			} `json:"pageInfo"`
		}
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			_ = resp.Body.Close()
			return nil, fmt.Errorf("the Backstage catalog answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("the Backstage catalog: %w", err)
		}
		for _, it := range body.Items {
			out = append(out, CatalogComponent{
				Name: it.Metadata.Name, Service: it.Metadata.Annotations[BackstageServiceAnnotation],
				KubernetesID: it.Metadata.Annotations["backstage.io/kubernetes-id"], Team: backstageTeam(it.Spec.Owner),
			})
		}
		if cursor = body.PageInfo.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

// backstageTeam reads a team from an owner reference: group:default/team-payments, group:team-payments or
// team-payments (a group by default). A person (user:…) is no team.
func backstageTeam(ref string) string {
	ref = strings.TrimSpace(ref)
	kind, name, found := strings.Cut(ref, ":")
	if !found {
		name, kind = ref, "group"
	}
	if !strings.EqualFold(kind, "group") {
		return ""
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}
