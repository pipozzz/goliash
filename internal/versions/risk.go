// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"fmt"

	"github.com/pipozzz/goliash/internal/store"
)

// Upgrade risk: how much an upgrade may break, as far as Goliash can tell from what it knows.
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

// WithRisk weighs each upgrade: what its release notes say breaks, how big the jump is, the stops on its way, the
// exploited vulnerabilities it brings, and whether the version to move to runs already somewhere else (another
// environment or target), which takes it down a level.
func WithRisk(us []Update, active []store.Instance) []Update {
	type key struct{ svc, tag string }
	where := map[key]map[string]bool{} // the environments each service runs each version in
	for _, in := range active {
		if in.ServiceID == "" || !in.IsMain {
			continue
		}
		k := key{in.ServiceID, ParseImage(in.Image).Tag}
		if where[k] == nil {
			where[k] = map[string]bool{}
		}
		where[k][in.EnvironmentID] = true
	}
	for i := range us {
		u := &us[i]
		if u.Target == "" {
			continue
		}
		proven := false
		for env := range where[key{u.Service.ID, u.Target}] {
			if env != u.Environment.ID || len(u.On) > 0 {
				proven = true
			}
		}
		u.Risk, u.RiskReasons = riskOf(*u, proven)
	}
	return us
}

// riskOf is an upgrade's risk and why.
func riskOf(u Update, proven bool) (string, []string) {
	level := 0
	var why []string
	raise := func(to int, reason string) {
		level = max(level, to)
		why = append(why, reason)
	}
	switch u.Jump {
	case JumpMajor:
		raise(1, "a major version")
	case JumpMinor:
		why = append(why, "a minor version")
	default:
		why = append(why, "a patch")
	}
	if n := len(u.Breaking); n > 0 {
		raise(2, fmt.Sprintf("its release notes say something breaks (%d)", n))
	}
	if len(u.Path) > 1 {
		raise(1, fmt.Sprintf("%d steps on the way", len(u.Path)))
	}
	if x := Exploited(u.Vulns.New); x > 0 {
		raise(2, fmt.Sprintf("brings %d exploited vulnerabilities", x))
	}
	if proven {
		if level > 0 {
			level--
		}
		why = append(why, u.Target+" runs already elsewhere")
	}
	return []string{RiskLow, RiskMedium, RiskHigh}[level], why
}
