// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"fmt"
	"time"
)

// hoursPerMonth is AWS's month for pricing: 730 hours.
const hoursPerMonth = 730

// AWS list prices in US East (N. Virginia), in US dollars an hour; other regions cost about the same or a little
// more. RDS and Aurora charge Extended Support per vCPU, twice as much from its third year; EKS charges $0.60 an
// hour for a cluster in extended support instead of $0.10.
const (
	rdsExtendedRate      = 0.10
	rdsExtendedRateYear3 = 0.20
	eksExtendedExtra     = 0.50
)

// ExtendedSupport is what AWS charges, on top, for a managed service it keeps running past the end of standard
// support.
type ExtendedSupport struct {
	From    time.Time // when it starts (or started) charging: the end of standard support
	Monthly float64   // US dollars a month at US East list prices; 0 when Goliash cannot tell (it still charges)
	Basis   string    // how: "4 vCPU × $0.10 an hour"
}

// Started says whether AWS charges for it already.
func (e ExtendedSupport) Started(now time.Time) bool { return !now.Before(e.From) }

// String says it in a few words: "$292/month from 2027-03-01 (4 vCPU × $0.10 an hour)".
func (e ExtendedSupport) String() string {
	cost := "charged"
	if e.Monthly > 0 {
		cost = Dollars(e.Monthly) + "/month"
	}
	s := cost + " from " + e.From.Format(time.DateOnly)
	if e.Basis != "" {
		s += " (" + e.Basis + ")"
	}
	return s
}

// Dollars writes an amount the way people read a bill: $292, $1,168.
func Dollars(v float64) string {
	n := int64(v + 0.5)
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return "$" + s
}

// ExtendedSupportCost is what AWS charges for repo (a managed engine) run past eol (the end of standard support,
// YYYY-MM-DD) on vcpus (RDS; 0 when not known), as of now. ok is false for what AWS has no Extended Support for:
// images, MariaDB, Valkey, Memcached.
func ExtendedSupportCost(repo, eol string, vcpus int, now time.Time) (ExtendedSupport, bool) {
	from, err := time.Parse(time.DateOnly, eol)
	if err != nil {
		return ExtendedSupport{}, false
	}
	es := ExtendedSupport{From: from}
	switch repo {
	case "rds.amazonaws.com/postgres", "rds.amazonaws.com/mysql", "rds.amazonaws.com/aurora-postgresql", "rds.amazonaws.com/aurora-mysql":
		if vcpus <= 0 {
			es.Basis = "per vCPU and hour"
			return es, true
		}
		rate := rdsExtendedRate
		if !now.Before(from.AddDate(2, 0, 0)) {
			rate = rdsExtendedRateYear3
		}
		es.Monthly = float64(vcpus) * rate * hoursPerMonth
		es.Basis = fmt.Sprintf("%d vCPU × $%.2f an hour", vcpus, rate)
	case "eks.amazonaws.com/kubernetes":
		es.Monthly = eksExtendedExtra * hoursPerMonth
		es.Basis = fmt.Sprintf("$%.2f an hour more for the cluster", eksExtendedExtra)
	case "elasticache.amazonaws.com/redis", "opensearch.amazonaws.com/opensearch", "opensearch.amazonaws.com/elasticsearch":
		es.Basis = "a share of the node price"
	default:
		return ExtendedSupport{}, false
	}
	return es, true
}
