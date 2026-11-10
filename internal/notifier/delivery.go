// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// EventDeliveryBrief sends, daily or on Mondays, how the last day's deploys went: what went out where, what was
// rolled back, what is stuck rolling out or down now, and how many low-risk upgrades are ready. Nothing deployed and
// nothing ready, nothing sent.
const EventDeliveryBrief = "delivery_brief"

// DeliveryBrief is the last day of deploys in a workspace.
type DeliveryBrief struct {
	Deploys   map[string]int // version changes by environment
	Rollbacks []string       // "payments 1.28.0 → 1.27.2 in prod"
	Stuck     []string       // "traefik stuck rolling out in prod (demo-prod-eu)"
	LowRisk   int            // upgrades not put off, low risk
	Troubled  int            // upgrades to a release that gave trouble elsewhere
}

// Any reports whether there is something to say.
func (d DeliveryBrief) Any() bool {
	return len(d.Deploys) > 0 || len(d.Rollbacks) > 0 || len(d.Stuck) > 0 || d.LowRisk > 0
}

// LoadDeliveryBrief gathers the last day of deploys before now.
func LoadDeliveryBrief(ctx context.Context, st *store.Store, sc store.Scope, now time.Time) (DeliveryBrief, error) {
	d := DeliveryBrief{Deploys: map[string]int{}}
	o, err := versions.LoadOverview(ctx, st, sc)
	if err != nil {
		return d, err
	}
	evs, err := st.ListEvents(ctx, sc, store.EventFilter{Types: []string{"deployed", "version_changed"}, Since: now.Add(-25 * time.Hour), Limit: 5000})
	if err != nil {
		return d, err
	}
	since := now.Add(-24 * time.Hour)
	for i, e := range evs {
		if e.At.Before(since) || e.ServiceID == "" || e.ToVersion == "" || e.FromVersion == e.ToVersion {
			continue
		}
		env := o.Envs[e.EnvironmentID].Name
		if versions.EventNote(e) == versions.RollbackNote {
			if versions.FlapBack(evs, i) {
				continue
			}
			d.Rollbacks = append(d.Rollbacks, fmt.Sprintf("%s %s → %s in %s", o.Services[e.ServiceID].Name, e.FromVersion, e.ToVersion, env))
		}
		d.Deploys[env]++
	}
	sort.Strings(d.Rollbacks)
	if issues, err := versions.RolloutIssues(ctx, st, sc, o, now); err == nil {
		for _, is := range issues {
			switch is.Kind {
			case versions.RolloutStuck:
				d.Stuck = append(d.Stuck, fmt.Sprintf("%s stuck rolling out in %s (%s)", is.Service, is.Environment, is.Target))
			case versions.RolloutDown:
				d.Stuck = append(d.Stuck, fmt.Sprintf("%s down in %s (%s)", is.Service, is.Environment, is.Target))
			}
		}
	}
	acks, _ := st.ListAcks(ctx, sc)
	for _, u := range versions.LoadWork(ctx, st, sc, o, acks, now) {
		switch {
		case u.Acked || u.Target == "":
		case u.Trouble != "":
			d.Troubled++
		case u.Risk == versions.RiskLow:
			d.LowRisk++
		}
	}
	return d, nil
}

// Text is the brief in a few sentences, environments in their order.
func (d DeliveryBrief) Text(envOrder []string) string {
	if !d.Any() {
		return "A quiet day: nothing deployed, nothing stuck, no low-risk upgrade waiting."
	}
	var parts []string
	if n := sumDeploys(d.Deploys); n > 0 {
		var per []string
		for _, e := range envOrder {
			if c := d.Deploys[e]; c > 0 {
				per = append(per, fmt.Sprintf("%s %d", e, c))
			}
		}
		parts = append(parts, fmt.Sprintf("Since yesterday: %s (%s).", plural(n, "deploy", "deploys"), strings.Join(per, ", ")))
	} else {
		parts = append(parts, "Nothing deployed since yesterday.")
	}
	if len(d.Rollbacks) > 0 {
		parts = append(parts, fmt.Sprintf("%s: %s.", capFirst(plural(len(d.Rollbacks), "rollback", "rollbacks")), named(d.Rollbacks)))
	}
	if len(d.Stuck) > 0 {
		parts = append(parts, "Now: "+named(d.Stuck)+".")
	}
	if d.LowRisk > 0 {
		parts = append(parts, fmt.Sprintf("%s ready for today.", capFirst(plural(d.LowRisk, "low-risk upgrade", "low-risk upgrades"))))
	}
	if d.Troubled > 0 {
		parts = append(parts, fmt.Sprintf("%s to a release that gave trouble elsewhere: look first.", capFirst(plural(d.Troubled, "upgrade", "upgrades"))))
	}
	return strings.Join(parts, " ")
}

func sumDeploys(m map[string]int) int {
	n := 0
	for _, c := range m {
		n += c
	}
	return n
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// deliveryItem is the delivery brief as one item, and whether it has anything to say.
func (n *Notifier) deliveryItem(ctx context.Context, sc store.Scope, now time.Time) (Item, bool, error) {
	d, err := LoadDeliveryBrief(ctx, n.store, sc, now)
	if err != nil {
		return Item{}, false, err
	}
	envs, err := n.store.ListEnvironments(ctx, sc)
	if err != nil {
		return Item{}, false, err
	}
	order := make([]string, len(envs))
	for i, e := range envs {
		order[i] = e.Name
	}
	return Item{Type: EventDeliveryBrief, At: now, URL: "/updates?q=low+risk", Text: d.Text(order)}, d.Any(), nil
}

// deliveryWorkspace queues the delivery brief for every due rule, once per period.
func (n *Notifier) deliveryWorkspace(ctx context.Context, sc store.Scope, rules []store.Rule, now time.Time) error {
	it, worth, err := n.deliveryItem(ctx, sc, now)
	if err != nil || !worth {
		return err
	}
	payload, err := json.Marshal(it)
	if err != nil {
		return err
	}
	for _, r := range rules {
		var f Filter
		_ = json.Unmarshal(r.Filter, &f)
		period := lastPlanTime(r.Mode, f, now).Format("2006-01-02")
		if _, err := n.store.Enqueue(ctx, store.QueueItem{
			Scope: sc, RuleID: r.ID, Payload: payload, DueAt: now, DedupKey: "delivery|" + period,
		}); err != nil {
			return err
		}
	}
	return nil
}
