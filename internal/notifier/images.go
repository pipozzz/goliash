// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// A deploy whose image is much bigger than the one it replaced is worth a look: a heavier base, a dependency
// pulled in by accident, build tools left in the final image. It costs pull time on every node and widens what
// can be attacked.

// Rule event types about running images.
const (
	EventImageGrew       = "image_grew"        // a deploy made the image much bigger
	EventImageOverBudget = "image_over_budget" // a deploy's image is bigger than its service's size budget
	EventOutdatedBase    = "outdated_base"     // an image is built on a base build weeks older than the base's tag
	EventEvidenceLost    = "evidence_lost"     // a deploy's image lacks a signature, SBOM or provenance the one before had
)

// imageEvents are the events CheckImages sends.
var imageEvents = []string{EventImageGrew, EventImageOverBudget, EventOutdatedBase, EventEvidenceLost}

// baseLag is how long an image may trail its base's newest build before it is announced.
const baseLag = 14 * 24 * time.Hour

const (
	growthRatio  = 1.25               // at least a quarter bigger …
	growthBytes  = 20_000_000         // … and at least 20 MB more
	growthRecent = 7 * 24 * time.Hour // only deploys this recent, so a first run does not report old news
	growthWindow = 30 * 24 * time.Hour
)

// CheckImages queues alerts about running images: for each service and environment whose image, deployed in the
// last week, grew a lot, is over its size budget or lost a signature, SBOM or provenance the one before had; and
// for images on a base build that has had a newer one for weeks. Once per image and environment.
func (n *Notifier) CheckImages(ctx context.Context) error {
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
			wanted = wanted || len(r.EventTypes) == 0 || containsAny(r.EventTypes, imageEvents)
		}
		if !wanted {
			continue
		}
		items, err := n.imageGrowth(ctx, sc, now)
		if err != nil {
			return err
		}
		for _, g := range items {
			if err := n.enqueue(ctx, sc, rules, g.item, g.item.Type+"|"+g.env+"|"+g.digest); err != nil {
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

// imageGrowth checks the images deployed in the last week against the ones they replaced (size, evidence) and
// their service's size budget, and every running image's base.
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
		svc := svcByID[ps[cur].ServiceID]
		env := envName[ps[cur].EnvironmentID]
		curEv := evidence[versions.ParseImage(ps[cur].Image).Repo()+"@"+ps[cur].Digest]
		add := func(it Item) {
			it.Service, it.Owner, it.Environment, it.At = svc.Name, svc.Owner, env, ps[cur].Since
			if ps[cur].Tag != "" && it.To == "" {
				it.To = ps[cur].Tag
			}
			it.Text = Describe(it)
			out = append(out, growth{item: it, env: ps[cur].EnvironmentID, digest: ps[cur].Digest})
		}
		if b := versions.SizeBudget(svc); b > 0 && curEv.Size > b {
			add(Item{Type: EventImageOverBudget, From: humanSize(curEv.Size), Note: humanSize(b) + " budget", To: ps[cur].Tag})
		}
		if prev < 0 {
			continue
		}
		prevEv := evidence[versions.ParseImage(ps[prev].Image).Repo()+"@"+ps[prev].Digest]
		if !curEv.CheckedAt.IsZero() && curEv.Error == "" {
			var lost []string
			if prevEv.Signed && !curEv.Signed {
				lost = append(lost, "signature")
			}
			if prevEv.SBOM && !curEv.SBOM {
				lost = append(lost, "SBOM")
			}
			if prevEv.Provenance && !curEv.Provenance {
				lost = append(lost, "provenance")
			}
			if len(lost) > 0 {
				add(Item{Type: EventEvidenceLost, Note: strings.Join(lost, ", "), From: ps[prev].Tag, To: ps[cur].Tag})
			}
		}
		before, after := prevEv.Size, curEv.Size
		if before <= 0 || after <= 0 || after-before < growthBytes || float64(after) < float64(before)*growthRatio {
			continue
		}
		note := fmt.Sprintf("+%s, +%d%%", humanSize(after-before), (after-before)*100/before)
		if ps[cur].Tag != "" {
			note += " with " + ps[cur].Tag
		}
		add(Item{Type: EventImageGrew, From: humanSize(before), To: humanSize(after), Note: note})
	}
	out = append(out, outdatedBases(periods, evidence, svcByID, envName, now)...)
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

// outdatedBases announces running images built on a base build that has had a newer one for over baseLag, once
// per image and environment.
func outdatedBases(periods []store.ImagePeriod, evidence map[string]store.ImageEvidence, svcByID map[string]store.Service, envName map[string]string, now time.Time) []growth {
	var out []growth
	seen := map[string]bool{}
	for _, p := range periods {
		if !p.Until.IsZero() || seen[p.EnvironmentID+"|"+p.Digest] {
			continue
		}
		ev := evidence[versions.ParseImage(p.Image).Repo()+"@"+p.Digest]
		if ev.BaseStatus != "outdated" || ev.BaseSince.IsZero() || now.Sub(ev.BaseSince) < baseLag {
			continue
		}
		seen[p.EnvironmentID+"|"+p.Digest] = true
		svc := svcByID[p.ServiceID]
		base := strings.TrimPrefix(strings.TrimPrefix(ev.BaseImage, "docker.io/library/"), "docker.io/")
		it := Item{
			Type: EventOutdatedBase, Service: svc.Name, Owner: svc.Owner, Environment: envName[p.EnvironmentID], At: now,
			From: base, To: p.Tag, Note: fmt.Sprintf("%d days", int(now.Sub(ev.BaseSince).Hours()/24)),
		}
		it.Text = Describe(it)
		out = append(out, growth{item: it, env: p.EnvironmentID, digest: p.Digest})
	}
	return out
}
