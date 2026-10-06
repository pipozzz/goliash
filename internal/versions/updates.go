// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Update is one thing to upgrade: a service (within one application, when it is
// compared per application) in one environment, from what runs to what it should.
type Update struct {
	Service     store.Service
	App         string
	Environment store.Environment
	Running     string
	Target      string // the version to move to; empty when only the end of life is known
	TargetURL   string // its release notes
	Jump        Jump
	Behind      string // the previous environment it is behind, if any
	EOL         string // the end-of-life date of the running release cycle, if it is near or past
	EOLPassed   bool
	Since       time.Time // when the oldest of its drifts opened
	Acked       bool      // an acknowledgement quiets it
	Urgency     int       // 0 most urgent; see urgencyLabels
}

// Urgency levels, most urgent first.
const (
	UrgencyEOLPassed = iota
	UrgencyEOLSoon
	UrgencyMajor
	UrgencyMinor
	UrgencyBehindEnv
	UrgencyPatch
)

// UrgencyLabels name the levels.
var UrgencyLabels = []string{"end of life", "end of life soon", "major", "minor", "behind previous env", "patch"}

// Updates lists what to upgrade, most urgent first: open end-of-life, upstream and
// environment drift, one item per service, application and environment.
func Updates(o Overview, acks []store.Ack, now time.Time) []Update {
	type key struct{ svc, app, env string }
	byKey := map[key]*Update{}
	var order []key
	for _, ds := range o.Drifts {
		for _, d := range ds {
			if d.Kind != "eol" && d.Kind != "upstream" && d.Kind != "env" {
				continue
			}
			k := key{d.ServiceID, d.App, d.EnvironmentID}
			u := byKey[k]
			if u == nil {
				u = &Update{Service: o.Services[d.ServiceID], App: d.App, Environment: o.Envs[d.EnvironmentID], Since: d.Since, Urgency: UrgencyPatch}
				byKey[k] = u
				order = append(order, k)
			}
			if d.Since.Before(u.Since) {
				u.Since = d.Since
			}
			var det DriftDetail
			_ = json.Unmarshal(d.Detail, &det)
			if det.Running != "" {
				u.Running = det.Running
			}
			switch d.Kind {
			case "upstream":
				u.Target, u.Jump = det.Other, det.Jump
			case "env":
				u.Behind = det.OtherIn
				if u.Target == "" {
					u.Target = det.Other
				}
				if u.Jump == JumpNone {
					u.Jump = det.Jump
				}
			case "eol":
				u.EOL = det.EOL
				if t, err := time.Parse("2006-01-02", det.EOL); err != nil || !t.After(now) {
					u.EOLPassed = true
				}
			}
		}
	}
	out := make([]Update, 0, len(order))
	for _, k := range order {
		u := byKey[k]
		if u.Target == "" { // end of life only: point at the newest acceptable release
			if up, ok := o.Upstreams[k.svc]; ok && up.HasLatest {
				u.Target = up.Latest.Raw
			}
		}
		if u.Target != "" {
			u.TargetURL = o.ReleaseURL[k.svc+"|"+u.Target]
		}
		u.Urgency = urgency(*u)
		u.Acked = ackedUpdate(*u, acks, now)
		out = append(out, *u)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Urgency != b.Urgency {
			return a.Urgency < b.Urgency
		}
		if a.Environment.Position != b.Environment.Position {
			return a.Environment.Position > b.Environment.Position // production first
		}
		if !a.Since.Equal(b.Since) {
			return a.Since.Before(b.Since)
		}
		if a.Service.Name != b.Service.Name {
			return a.Service.Name < b.Service.Name
		}
		return a.App < b.App
	})
	return out
}

func urgency(u Update) int {
	switch {
	case u.EOL != "" && u.EOLPassed:
		return UrgencyEOLPassed
	case u.EOL != "":
		return UrgencyEOLSoon
	case u.Jump == JumpMajor:
		return UrgencyMajor
	case u.Jump == JumpMinor:
		return UrgencyMinor
	case u.Behind != "":
		return UrgencyBehindEnv
	}
	return UrgencyPatch
}

// ackedUpdate reports whether a drift acknowledgement for the service (in this or
// every environment) is still in force: until a date, or until a version newer than
// the target appears.
func ackedUpdate(u Update, acks []store.Ack, now time.Time) bool {
	for _, a := range acks {
		if a.Kind != "drift" || a.ServiceID != u.Service.ID || (a.EnvironmentID != "" && a.EnvironmentID != u.Environment.ID) {
			continue
		}
		if !a.UntilAt.IsZero() && now.Before(a.UntilAt) {
			return true
		}
		if a.UntilVersion != "" {
			to, okTo := ParseVersion(u.Target)
			until, okUntil := ParseVersion(a.UntilVersion)
			if okTo && okUntil && to.Compare(until) < 0 {
				return true
			}
		}
	}
	return false
}
