// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Delivery is how a service moves through the environments, read from what ran when:
// how many versions arrived in each environment and how long a version took from one
// environment to the next.
type Delivery struct {
	Service   store.Service
	Envs      []EnvDelivery // in promotion order
	LeadTimes []LeadTime    // between consecutive environments
}

// EnvDelivery is one environment of a service.
type EnvDelivery struct {
	Env        store.Environment
	Deploys    int       // versions that arrived within the window
	LastDeploy time.Time // zero when none is known
}

// LeadTime is how long versions took from one environment to the next (median).
type LeadTime struct {
	From, To store.Environment
	Median   time.Duration
	Samples  int
}

// maxDeliveryEvents bounds the history read for the statistics.
const maxDeliveryEvents = 50000

// DeliveryStats computes delivery statistics over the window ending at now. A version
// arrives in an environment when it is first seen there (deployed or version_changed);
// lead times pair a version's arrival with its arrival in the environment before.
func DeliveryStats(ctx context.Context, st *store.Store, sc store.Scope, o Overview, window time.Duration, now time.Time) ([]Delivery, error) {
	// Look back twice the window, so a version that reached staging just before the
	// window still counts when it reaches prod inside it.
	evs, err := st.ListEvents(ctx, sc, store.EventFilter{
		Types: []string{"deployed", "version_changed"}, Since: now.Add(-2 * window), Limit: maxDeliveryEvents,
	})
	if err != nil {
		return nil, err
	}
	type key struct{ service, env, version string }
	arrival := map[key]time.Time{}
	for _, e := range evs {
		if e.ServiceID == "" || e.ToVersion == "" || e.FromVersion == e.ToVersion {
			continue // unmapped, removed, or a retag of the same version
		}
		k := key{e.ServiceID, e.EnvironmentID, e.ToVersion}
		if t, ok := arrival[k]; !ok || e.At.Before(t) {
			arrival[k] = e.At
		}
	}
	type arr struct {
		env, version string
		at           time.Time
	}
	byService := map[string][]arr{}
	for k, t := range arrival {
		byService[k.service] = append(byService[k.service], arr{k.env, k.version, t})
	}
	since := now.Add(-window)
	var out []Delivery
	for _, row := range o.Matrix.Rows {
		d := Delivery{Service: row.Service}
		for _, env := range o.Matrix.Environments {
			ed := EnvDelivery{Env: env}
			for _, a := range byService[row.Service.ID] {
				if a.env != env.ID {
					continue
				}
				if !a.at.Before(since) {
					ed.Deploys++
				}
				if a.at.After(ed.LastDeploy) {
					ed.LastDeploy = a.at
				}
			}
			d.Envs = append(d.Envs, ed)
		}
		for i := 1; i < len(o.Matrix.Environments); i++ {
			from, to := o.Matrix.Environments[i-1], o.Matrix.Environments[i]
			var samples []time.Duration
			for _, a := range byService[row.Service.ID] {
				if a.env != to.ID || a.at.Before(since) {
					continue
				}
				if prev, ok := arrival[key{row.Service.ID, from.ID, a.version}]; ok && !prev.After(a.at) {
					samples = append(samples, a.at.Sub(prev))
				}
			}
			lt := LeadTime{From: from, To: to, Samples: len(samples)}
			if len(samples) > 0 {
				sort.Slice(samples, func(a, b int) bool { return samples[a] < samples[b] })
				lt.Median = samples[len(samples)/2]
				if len(samples)%2 == 0 {
					lt.Median = (samples[len(samples)/2-1] + samples[len(samples)/2]) / 2
				}
			}
			d.LeadTimes = append(d.LeadTimes, lt)
		}
		out = append(out, d)
	}
	return out, nil
}

// HumanDuration prints a lead time like 3d 4h, 5h or 40m.
func HumanDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		days, hours := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour)
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, hours)
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
}
