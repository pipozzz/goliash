// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// sizeDays is how far back a service's image size is drawn.
const sizeDays = 90

// serviceSizes draws a service's image size per environment over time: each day, the largest image that ran
// there that day, in MB, from when the first size is known. A step is a deploy that made the image bigger or
// smaller. Empty when no size is known.
func (s *Server) serviceSizes(ctx context.Context, sc store.Scope, svc store.Service, now time.Time) (LineChart, *SizeChange, error) {
	start := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(sizeDays - 1))
	periods, err := s.store.ServiceImagePeriods(ctx, sc, svc.ID, start)
	if err != nil || len(periods) == 0 {
		return LineChart{}, nil, err
	}
	evidence, err := s.store.ListImageEvidence(ctx, sc)
	if err != nil {
		return LineChart{}, nil, err
	}
	envs, err := s.store.ListEnvironments(ctx, sc)
	if err != nil {
		return LineChart{}, nil, err
	}
	change := lastSizeChange(periods, evidence, envs)
	// MB per environment and day.
	perEnv := map[string][]int{}
	first := sizeDays
	for _, p := range periods {
		size := evidence[versions.ParseImage(p.Image).Repo()+"@"+p.Digest].Size
		if size <= 0 {
			continue
		}
		mb := int((size + 500_000) / 1_000_000)
		days := perEnv[p.EnvironmentID]
		if days == nil {
			days = make([]int, sizeDays)
			perEnv[p.EnvironmentID] = days
		}
		for d := range sizeDays {
			end := start.AddDate(0, 0, d+1)
			if p.Since.Before(end) && (p.Until.IsZero() || p.Until.After(end.Add(-24*time.Hour))) && mb > days[d] {
				days[d] = mb
				first = min(first, d)
			}
		}
	}
	if first == sizeDays {
		return LineChart{}, change, nil
	}
	c := LineChart{}
	for d := first; d < sizeDays; d++ {
		c.Labels = append(c.Labels, start.AddDate(0, 0, d).Format("Jan 2"))
	}
	top := 0
	for i, e := range envs {
		days, ok := perEnv[e.ID]
		if !ok {
			continue
		}
		values := append([]int{}, days[first:]...)
		for d := 1; d < len(values); d++ { // a day without a known size keeps the one before
			if values[d] == 0 {
				values[d] = values[d-1]
			}
		}
		for _, v := range values {
			top = max(top, v)
		}
		c.Series = append(c.Series, Series{Name: e.Name, Class: colour(i), Values: values})
	}
	c.Max = niceMax(top)
	return c, change, nil
}

// SizeChange is what the last deploy in an environment changed in the image: its size and its layers.
type SizeChange struct {
	Env, From, To    string // environment, and the versions before and after
	Before, After    int64  // sizes in bytes
	Added, Removed   []store.ImageLayer
	AddedN, RemovedN int // all of them, when the lists are cut short
}

// sizeChangeLayers is how many added and removed layers a size change lists.
const sizeChangeLayers = 6

// lastSizeChange is the most recent deploy, production first, whose two images' layers are known.
func lastSizeChange(periods []store.ImagePeriod, evidence map[string]store.ImageEvidence, envs []store.Environment) *SizeChange {
	ev := func(p store.ImagePeriod) store.ImageEvidence {
		return evidence[versions.ParseImage(p.Image).Repo()+"@"+p.Digest]
	}
	for i := len(envs) - 1; i >= 0; i-- {
		var cur, prev *store.ImagePeriod
		for j := range periods {
			p := &periods[j]
			if p.EnvironmentID == envs[i].ID && p.Until.IsZero() && (cur == nil || p.Since.After(cur.Since)) {
				cur = p
			}
		}
		if cur == nil {
			continue
		}
		for j := range periods {
			p := &periods[j]
			if p.EnvironmentID == envs[i].ID && p.Digest != cur.Digest && !p.Until.IsZero() && (prev == nil || p.Until.After(prev.Until)) {
				prev = p
			}
		}
		if prev == nil || len(ev(*cur).Layers) == 0 || len(ev(*prev).Layers) == 0 {
			continue
		}
		added, removed := versions.LayerChange(ev(*prev).Layers, ev(*cur).Layers)
		ch := &SizeChange{
			Env: envs[i].Name, From: prev.Tag, To: cur.Tag, Before: ev(*prev).Size, After: ev(*cur).Size,
			AddedN: len(added), RemovedN: len(removed),
		}
		ch.Added, ch.Removed = added[:min(len(added), sizeChangeLayers)], removed[:min(len(removed), sizeChangeLayers)]
		return ch
	}
	return nil
}
