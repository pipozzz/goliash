// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// maxAttempts is how often a delivery is tried before it is given up.
const maxAttempts = 6

// Item is one thing to tell people about.
type Item struct {
	Type        string    `json:"type"` // an event type, or agent_stale
	Service     string    `json:"service,omitempty"`
	App         string    `json:"app,omitempty"` // a drift within one application of a shared service
	Owner       string    `json:"owner,omitempty"`
	Environment string    `json:"environment,omitempty"`
	Target      string    `json:"target,omitempty"`
	From        string    `json:"from,omitempty"`
	To          string    `json:"to,omitempty"`
	Note        string    `json:"note,omitempty"`
	At          time.Time `json:"at"`
	Text        string    `json:"text"`
	URL         string    `json:"url,omitempty"` // release notes of a new release
}

// Message is what one delivery sends: one item, or a digest of many.
type Message struct {
	Workspace string `json:"workspace"`
	Digest    bool   `json:"digest"`
	Items     []Item `json:"items"`
	Link      string `json:"link,omitempty"` // Goliash's public URL, for buttons back to it
}

// Title is a one-line summary of the message.
func (m Message) Title() string {
	if m.plan() {
		return fmt.Sprintf("Goliash upgrade plan for %s: %d to upgrade", m.Workspace, len(m.Items))
	}
	if len(m.Items) == 1 && !m.Digest {
		return m.Items[0].Text
	}
	return fmt.Sprintf("Goliash: %d updates in %s", len(m.Items), m.Workspace)
}

// plan reports whether the message is an upgrade plan.
func (m Message) plan() bool {
	for _, it := range m.Items {
		if it.Type != "update" {
			return false
		}
	}
	return len(m.Items) > 0
}

// Sender delivers a message through one channel type.
type Sender interface {
	Send(ctx context.Context, ch store.Channel, msg Message) error
}

// Filter narrows a rule. Empty lists match everything.
type Filter struct {
	Services     []string      `json:"services,omitempty"`
	Owners       []string      `json:"owners,omitempty"`
	Environments []string      `json:"environments,omitempty"`
	MinJump      versions.Jump `json:"min_jump,omitempty"` // for new_release: smallest version jump to report
	DigestHour   *int          `json:"digest_hour,omitempty"`
}

// Notifier routes events to channels according to rules, acks and digests.
type Notifier struct {
	store   *store.Store
	log     *slog.Logger
	senders map[string]Sender
	now     func() time.Time
	link    string // public URL, without a trailing slash
}

// SetPublicURL lets messages link back to Goliash (buttons, item links).
func (n *Notifier) SetPublicURL(u string) { n.link = strings.TrimSuffix(u, "/") }

// New returns a notifier with the given senders by channel type.
func New(st *store.Store, log *slog.Logger, senders map[string]Sender) *Notifier {
	return &Notifier{store: st, log: log, senders: senders, now: func() time.Time { return time.Now().UTC() }}
}

// names resolves IDs in events to what people read.
type names struct {
	services map[string]store.Service
	envs     map[string]string
	targets  map[string]string
}

func (n *Notifier) loadNames(ctx context.Context, sc store.Scope) (names, error) {
	nm := names{services: map[string]store.Service{}, envs: map[string]string{}, targets: map[string]string{}}
	services, err := n.store.ListServices(ctx, sc)
	if err != nil {
		return nm, err
	}
	for _, s := range services {
		nm.services[s.ID] = s
	}
	envs, err := n.store.ListEnvironments(ctx, sc)
	if err != nil {
		return nm, err
	}
	for _, e := range envs {
		nm.envs[e.ID] = e.Name
	}
	targets, err := n.store.ListTargets(ctx, sc)
	if err != nil {
		return nm, err
	}
	for _, t := range targets {
		nm.targets[t.ID] = t.Name
	}
	return nm, nil
}

// Handle queues notifications for events. It is the callback for snapshot
// processing and the version checker, and never blocks on delivery.
func (n *Notifier) Handle(sc store.Scope, events []store.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := n.handle(ctx, sc, events); err != nil {
		n.log.Error("queueing notifications failed", "err", err)
	}
}

func (n *Notifier) handle(ctx context.Context, sc store.Scope, events []store.Event) error {
	rules, err := n.store.ActiveRules(ctx, sc)
	if err != nil || len(rules) == 0 {
		return err
	}
	nm, err := n.loadNames(ctx, sc)
	if err != nil {
		return err
	}
	acks, err := n.store.ListAcks(ctx, sc)
	if err != nil {
		return err
	}
	for _, e := range events {
		svc := nm.services[e.ServiceID]
		if acked(e, acks, n.now()) {
			n.log.Debug("notification suppressed by ack", "type", e.Type, "service", svc.Name)
			continue
		}
		item := Item{
			Type: e.Type, Service: svc.Name, App: e.App, Owner: svc.Owner, Environment: nm.envs[e.EnvironmentID],
			Target: nm.targets[e.TargetID], From: e.FromVersion, To: e.ToVersion, Note: e.Note, At: e.At,
		}
		item.Text = Describe(item)
		if e.Type == "new_release" {
			if rel, err := n.store.GetRelease(ctx, sc, e.ServiceID, e.ToVersion); err == nil {
				item.URL = rel.ChangelogURL
			}
		}
		dedup := e.ID
		if e.Type == "new_release" {
			dedup = "new_release|" + e.ServiceID + "|" + e.ToVersion // once per service and version
		}
		if dedup == "" {
			dedup = fmt.Sprintf("%s|%s|%s|%s|%d", e.Type, e.ServiceID, e.EnvironmentID, e.ToVersion, e.At.UnixNano())
		}
		if err := n.enqueue(ctx, sc, rules, item, dedup); err != nil {
			return err
		}
	}
	return nil
}

// AgentStale notifies about an agent that stopped sending heartbeats.
func (n *Notifier) AgentStale(a store.Agent) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rules, err := n.store.ActiveRules(ctx, a.Scope)
	if err != nil {
		n.log.Error("queueing notifications failed", "err", err)
		return
	}
	item := Item{Type: "agent_stale", Target: a.Name, At: a.StaleSince}
	item.Text = Describe(item)
	if err := n.enqueue(ctx, a.Scope, rules, item, "agent_stale|"+a.ID+"|"+a.StaleSince.Format(time.RFC3339)); err != nil {
		n.log.Error("queueing notifications failed", "err", err)
	}
}

func (n *Notifier) enqueue(ctx context.Context, sc store.Scope, rules []store.Rule, item Item, dedup string) error {
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	for _, r := range rules {
		var f Filter
		if err := json.Unmarshal(r.Filter, &f); err != nil {
			n.log.Warn("notification rule has an invalid filter", "rule_id", r.ID, "err", err)
			continue
		}
		if !matches(r, f, item) {
			continue
		}
		added, err := n.store.Enqueue(ctx, store.QueueItem{
			Scope: sc, RuleID: r.ID, Payload: payload, DedupKey: dedup, DueAt: dueAt(r.Mode, f, n.now()),
		})
		if err != nil {
			return err
		}
		if added {
			n.log.Debug("notification queued", "rule_id", r.ID, "mode", r.Mode, "text", item.Text)
		}
	}
	return nil
}

func matches(r store.Rule, f Filter, it Item) bool {
	if len(r.EventTypes) > 0 && !contains(r.EventTypes, it.Type) {
		return false
	}
	if it.Type == "agent_stale" { // not tied to a service or environment
		return true
	}
	if len(f.Services) > 0 && !contains(f.Services, it.Service) {
		return false
	}
	if len(f.Owners) > 0 && !contains(f.Owners, it.Owner) {
		return false
	}
	if len(f.Environments) > 0 && it.Environment != "" && !contains(f.Environments, it.Environment) {
		return false
	}
	if f.MinJump != versions.JumpNone && it.Type == "new_release" {
		if !versions.Jump(it.Note).AtLeast(f.MinJump) {
			return false
		}
	}
	return true
}

// acked reports whether an acknowledgement quiets the event.
func acked(e store.Event, acks []store.Ack, now time.Time) bool {
	kind := ""
	switch e.Type {
	case "new_release":
		kind = "release"
	case "drift_detected":
		kind = "drift"
	default:
		return false
	}
	for _, a := range acks {
		if a.Kind != kind || a.ServiceID != e.ServiceID {
			continue
		}
		if a.EnvironmentID != "" && a.EnvironmentID != e.EnvironmentID {
			continue
		}
		if !a.UntilAt.IsZero() && now.Before(a.UntilAt) {
			return true
		}
		if a.UntilVersion != "" {
			to, okTo := versions.ParseVersion(e.ToVersion)
			until, okUntil := versions.ParseVersion(a.UntilVersion)
			if okTo && okUntil && to.Compare(until) < 0 {
				return true
			}
		}
	}
	return false
}

// dueAt is when an item for a rule should go out: now, or the next digest.
func dueAt(mode string, f Filter, now time.Time) time.Time {
	hour := 8
	if f.DigestHour != nil && *f.DigestHour >= 0 && *f.DigestHour < 24 {
		hour = *f.DigestHour
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.UTC)
	switch mode {
	case "daily":
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next
	case "weekly":
		for next.Weekday() != time.Monday || !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next
	}
	return now
}

// Run delivers due notifications every interval until ctx ends, and queues the
// upgrade plans that are due every few minutes.
func (n *Notifier) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var planned time.Time
	for {
		if time.Since(planned) >= 5*time.Minute {
			planned = time.Now()
			if err := n.PlanUpdates(ctx); err != nil && ctx.Err() == nil {
				n.log.Error("queueing upgrade plans failed", "err", err)
			}
		}
		if err := n.DeliverDue(ctx); err != nil && ctx.Err() == nil {
			n.log.Error("delivering notifications failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// DeliverDue sends every due item, one message per rule.
func (n *Notifier) DeliverDue(ctx context.Context) error {
	items, err := n.store.DueItems(ctx, n.now(), maxAttempts)
	if err != nil || len(items) == 0 {
		return err
	}
	byRule := map[string][]store.QueueItem{}
	var ruleOrder []string
	for _, it := range items {
		if _, ok := byRule[it.RuleID]; !ok {
			ruleOrder = append(ruleOrder, it.RuleID)
		}
		byRule[it.RuleID] = append(byRule[it.RuleID], it)
	}
	cache := map[store.Scope]map[string]ruleTarget{}
	for _, ruleID := range ruleOrder {
		batch := byRule[ruleID]
		sc := batch[0].Scope
		if cache[sc] == nil {
			if cache[sc], err = n.ruleTargets(ctx, sc); err != nil {
				return err
			}
		}
		n.deliver(ctx, cache[sc][ruleID], batch)
	}
	return nil
}

type ruleTarget struct {
	rule      store.Rule
	channel   store.Channel
	workspace string
}

func (n *Notifier) ruleTargets(ctx context.Context, sc store.Scope) (map[string]ruleTarget, error) {
	rules, err := n.store.ListRules(ctx, sc)
	if err != nil {
		return nil, err
	}
	chans, err := n.store.ListChannels(ctx, sc)
	if err != nil {
		return nil, err
	}
	byID := map[string]store.Channel{}
	for _, c := range chans {
		byID[c.ID] = c
	}
	wsName := sc.WorkspaceID
	if all, err := n.store.ListWorkspaces(ctx); err == nil {
		for _, w := range all {
			if w.ID == sc.WorkspaceID {
				wsName = w.Name
			}
		}
	}
	out := map[string]ruleTarget{}
	for _, r := range rules {
		out[r.ID] = ruleTarget{rule: r, channel: byID[r.ChannelID], workspace: wsName}
	}
	return out, nil
}

func (n *Notifier) deliver(ctx context.Context, rt ruleTarget, batch []store.QueueItem) {
	ids := make([]string, len(batch))
	msg := Message{Workspace: rt.workspace, Digest: rt.rule.Mode != "instant" || len(batch) > 1, Link: n.link}
	attempts := 0
	for i, it := range batch {
		ids[i] = it.ID
		attempts = max(attempts, it.Attempts)
		var item Item
		if err := json.Unmarshal(it.Payload, &item); err == nil {
			msg.Items = append(msg.Items, item)
		}
	}
	sort.SliceStable(msg.Items, func(i, j int) bool { return msg.Items[i].At.Before(msg.Items[j].At) })

	sender, ok := n.senders[rt.channel.Type]
	var err error
	if !ok {
		err = fmt.Errorf("no sender for channel type %q", rt.channel.Type)
	} else {
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = sender.Send(sendCtx, rt.channel, msg)
		cancel()
		if errors.Is(err, ErrNoBrowsers) {
			err = nil // nobody to tell; not worth retrying
		}
	}
	if err == nil {
		if err := n.store.MarkSent(ctx, ids); err != nil {
			n.log.Error("marking notifications sent failed", "err", err)
		}
		n.log.Info("notification sent", "channel", rt.channel.Name, "items", len(ids), "digest", msg.Digest)
		return
	}
	retry := n.now().Add(time.Duration(1<<min(attempts, 6)) * time.Minute) // 1, 2, 4 … 64 min
	n.log.Warn("notification delivery failed", "channel", rt.channel.Name, "attempt", attempts+1, "err", err)
	if err := n.store.MarkFailed(ctx, ids, err.Error(), retry); err != nil {
		n.log.Error("recording failed delivery failed", "err", err)
	}
}

// SendTest sends a test message through a channel right away.
func (n *Notifier) SendTest(ctx context.Context, ch store.Channel, workspace string) error {
	sender, ok := n.senders[ch.Type]
	if !ok {
		return fmt.Errorf("no sender for channel type %q", ch.Type)
	}
	item := Item{Type: "test", At: n.now(), Text: "Goliash test notification for channel " + ch.Name}
	return sender.Send(ctx, ch, Message{Workspace: workspace, Items: []Item{item}, Link: n.link})
}

// Describe writes the one-line text people read about an item.
func Describe(it Item) string {
	where := it.Service
	if it.App != "" {
		where += " (" + it.App + ")"
	}
	if it.Environment != "" {
		where += " @ " + it.Environment
	}
	if where == "" {
		where = "unmapped workload"
	}
	on := ""
	if it.Target != "" {
		on = " on " + it.Target
	}
	switch it.Type {
	case "deployed":
		return fmt.Sprintf("%s: %s deployed%s", where, it.To, on)
	case "version_changed":
		if it.Note == "retag" {
			return fmt.Sprintf("%s: %s was re-pushed (new digest)%s", where, it.To, on)
		}
		return fmt.Sprintf("%s: %s → %s%s", where, it.From, it.To, on)
	case "removed":
		return fmt.Sprintf("%s: %s removed%s", where, it.From, on)
	case "new_release":
		jump := ""
		if it.Note != "" {
			jump = " (" + it.Note + ")"
		}
		return fmt.Sprintf("%s: new release %s%s, running %s", it.Service, it.To, jump, it.From)
	case "drift_detected":
		switch it.Note {
		case "env":
			return fmt.Sprintf("%s: runs %s, behind %s in the previous environment", where, it.From, it.To)
		case "upstream":
			return fmt.Sprintf("%s: runs %s, upstream has %s", where, it.From, it.To)
		case "inconsistent":
			return fmt.Sprintf("%s: targets run different versions", where)
		case "declared":
			return fmt.Sprintf("%s: runs %s, but Git declares %s", where, it.From, it.To)
		case "eol":
			return fmt.Sprintf("%s: runs %s, whose release cycle %s reaches or has reached its end of life", where, it.From, it.To)
		}
		return fmt.Sprintf("%s: drift (%s)", where, it.Note)
	case "drift_resolved":
		return fmt.Sprintf("%s: %s drift resolved", where, it.Note)
	case "update":
		to := ""
		if it.To != "" {
			to = " → " + it.To
		}
		return fmt.Sprintf("%s: %s%s (%s)", where, orDash(it.From), to, it.Note)
	case "agent_stale":
		return fmt.Sprintf("agent %s has not sent a heartbeat for 10 minutes; its targets are stale", it.Target)
	}
	return strings.TrimSpace(fmt.Sprintf("%s: %s %s %s", where, it.Type, it.From, it.To))
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ErrNoSMTP means e-mail was requested without SMTP settings.
var ErrNoSMTP = errors.New("e-mail is not configured: give the channel a mail server, or set GOLIASH_SMTP_ADDR and GOLIASH_SMTP_FROM")

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
