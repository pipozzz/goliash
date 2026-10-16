// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// A deploy whose image is much bigger than the one it replaced is worth a look: a heavier base, a dependency
// pulled in by accident, build tools left in the final image. It costs pull time on every node and widens what
// can be attacked.

// EventImageGrew is the rule event type of an image that grew a lot with a deploy.
const EventImageGrew = "image_grew"

const (
	growthRatio  = 1.25               // at least a quarter bigger …
	growthBytes  = 20_000_000         // … and at least 20 MB more
	growthRecent = 7 * 24 * time.Hour // only deploys this recent, so a first run does not report old news
	growthWindow = 30 * 24 * time.Hour
)

// CheckImageGrowth queues an alert for each service and environment whose image, deployed in the last week, is
// much bigger than the one it replaced, once per new image.
func (n *Notifier) CheckImageGrowth(ctx context.Context) error {
	wss, err := n.store.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	now := n.now()
	for _, ws := range wss {
		sc := ws.Scope()
		rules, err := n.store.ActiveRules(ctx, sc)
		if err != nil {
			return err
		}
		wanted := false
		for _, r := range rules {
			wanted = wanted || len(r.EventTypes) == 0 || contains(r.EventTypes, EventImageGrew)
		}
		if !wanted {
			continue
		}
		items, err := n.imageGrowth(ctx, sc, now)
		if err != nil {
			return err
		}
		for _, g := range items {
			if err := n.enqueue(ctx, sc, rules, g.item, "image_grew|"+g.env+"|"+g.digest); err != nil {
				return err
			}
		}
	}
	return nil
}

type growth struct {
	item        Item
	env, digest string
}

func (n *Notifier) imageGrowth(ctx context.Context, sc store.Scope, now time.Time) ([]growth, error) {
	periods, err := n.store.ImagePeriods(ctx, sc, now.Add(-growthWindow))
	if err != nil || len(periods) == 0 {
		return nil, err
	}
	evidence, err := n.store.ListImageEvidence(ctx, sc)
	if err != nil {
		return nil, err
	}
	services, err := n.store.ListServices(ctx, sc)
	if err != nil {
		return nil, err
	}
	envs, err := n.store.ListEnvironments(ctx, sc)
	if err != nil {
		return nil, err
	}
	svcByID, envName := map[string]store.Service{}, map[string]string{}
	for _, s := range services {
		svcByID[s.ID] = s
	}
	for _, e := range envs {
		envName[e.ID] = e.Name
	}
	size := func(p store.ImagePeriod) int64 {
		return evidence[versions.ParseImage(p.Image).Repo()+"@"+p.Digest].Size
	}
	byKey := map[string][]store.ImagePeriod{}
	for _, p := range periods {
		k := p.ServiceID + "|" + p.EnvironmentID
		byKey[k] = append(byKey[k], p)
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []growth
	for _, k := range keys {
		ps := byKey[k] // oldest first
		// The image running now that started most recently, and the last other image that ran before it.
		cur := -1
		for i, p := range ps {
			if p.Until.IsZero() && (cur < 0 || p.Since.After(ps[cur].Since)) {
				cur = i
			}
		}
		if cur < 0 || now.Sub(ps[cur].Since) > growthRecent {
			continue
		}
		prev := -1
		for i, p := range ps {
			if p.Digest != ps[cur].Digest && !p.Until.IsZero() && !p.Until.After(ps[cur].Since.Add(time.Hour)) &&
				(prev < 0 || p.Until.After(ps[prev].Until)) {
				prev = i
			}
		}
		if prev < 0 {
			continue
		}
		before, after := size(ps[prev]), size(ps[cur])
		if before <= 0 || after <= 0 || after-before < growthBytes || float64(after) < float64(before)*growthRatio {
			continue
		}
		svc := svcByID[ps[cur].ServiceID]
		it := Item{
			Type: EventImageGrew, Service: svc.Name, Owner: svc.Owner, Environment: envName[ps[cur].EnvironmentID],
			From: humanSize(before), To: humanSize(after), At: ps[cur].Since,
			Note: fmt.Sprintf("+%s, +%d%%", humanSize(after-before), (after-before)*100/before),
		}
		if ps[cur].Tag != "" {
			it.Note += " with " + ps[cur].Tag
		}
		it.Text = Describe(it)
		out = append(out, growth{item: it, env: ps[cur].EnvironmentID, digest: ps[cur].Digest})
	}
	return out, nil
}

// humanSize writes bytes as registries do, in decimal units.
func humanSize(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%.0f kB", float64(n)/1e3)
}
