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
func (s *Server) serviceSizes(ctx context.Context, sc store.Scope, svc store.Service, now time.Time) (LineChart, error) {
	start := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(sizeDays - 1))
	periods, err := s.store.ServiceImagePeriods(ctx, sc, svc.ID, start)
	if err != nil || len(periods) == 0 {
		return LineChart{}, err
	}
	evidence, err := s.store.ListImageEvidence(ctx, sc)
	if err != nil {
		return LineChart{}, err
	}
	envs, err := s.store.ListEnvironments(ctx, sc)
	if err != nil {
		return LineChart{}, err
	}
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
		return LineChart{}, nil
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
	return c, nil
}
