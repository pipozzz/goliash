// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tickets"
	"github.com/pipozzz/goliash/internal/versions"
)

// ticketCheckEvery is how often open tickets are checked for being done.
const ticketCheckEvery = 10 * time.Minute

// ticketHTTP is the client trackers are called with.
var ticketHTTP = &http.Client{Timeout: 30 * time.Second}

// ErrNoUpgrade is returned for a ticket asked for something with no upgrade to do: a ticket is filed per upgrade,
// and closed once it is deployed.
var ErrNoUpgrade = errors.New("nothing to upgrade there")

// fileTickets files the items of a message as tickets in the channel's tracker: one per upgrade (service,
// environment, application), commented on when it already has one. Items about no upgrade are left out.
func (n *Notifier) fileTickets(ctx context.Context, rt ruleTarget, msg Message) error {
	sc := rt.rule.Scope
	w, err := n.ticketWorld(ctx, sc)
	if err != nil {
		return err
	}
	for _, it := range msg.Items {
		svc, okS := w.services[it.Service]
		env, okE := w.envs[it.Environment]
		if !okS || !okE {
			continue
		}
		if _, err := n.fileTicket(ctx, rt.channel, sc, w, svc, env, it.App, it.Text); err != nil && !errors.Is(err, ErrNoUpgrade) {
			return err
		}
	}
	return nil
}

// FileTicket files a ticket for an upgrade in a tracker channel, by hand from the Updates page; one already open
// is returned as it is.
func (n *Notifier) FileTicket(ctx context.Context, sc store.Scope, channelID, serviceID, envID, app string) (store.Ticket, error) {
	ch, err := n.store.GetChannel(ctx, sc, channelID)
	if err != nil {
		return store.Ticket{}, err
	}
	if !tickets.Is(ch.Type) {
		return store.Ticket{}, fmt.Errorf("%s does not file tickets", ch.Name)
	}
	w, err := n.ticketWorld(ctx, sc)
	if err != nil {
		return store.Ticket{}, err
	}
	var svc store.Service
	var env store.Environment
	for _, s := range w.services {
		if s.ID == serviceID {
			svc = s
		}
	}
	for _, e := range w.envs {
		if e.ID == envID {
			env = e
		}
	}
	if svc.ID == "" || env.ID == "" {
		return store.Ticket{}, store.ErrNotFound
	}
	return n.fileTicket(ctx, ch, sc, w, svc, env, app, "")
}

// ticketWorld is what tickets are written from: the workspace's services, environments and upgrades.
type ticketWorld struct {
	o        versions.Overview
	updates  []versions.Update
	services map[string]store.Service     // by name
	envs     map[string]store.Environment // by name
}

func (n *Notifier) ticketWorld(ctx context.Context, sc store.Scope) (ticketWorld, error) {
	w := ticketWorld{services: map[string]store.Service{}, envs: map[string]store.Environment{}}
	o, err := versions.LoadOverview(ctx, n.store, sc)
	if err != nil {
		return w, err
	}
	acks, err := n.store.ListAcks(ctx, sc)
	if err != nil {
		return w, err
	}
	w.o, w.updates = o, versions.LoadUpdates(ctx, n.store, sc, o, acks, n.now())
	for _, s := range o.Services {
		w.services[s.Name] = s
	}
	for _, e := range o.Envs {
		w.envs[e.Name] = e
	}
	return w, nil
}

// update is the upgrade of a service in an environment (and application), or nil.
func (w ticketWorld) update(serviceID, envID, app string) *versions.Update {
	for i := range w.updates {
		u := &w.updates[i]
		if u.Service.ID == serviceID && u.Environment.ID == envID && (app == "" || u.App == app) {
			return u
		}
	}
	return nil
}

func (n *Notifier) fileTicket(ctx context.Context, ch store.Channel, sc store.Scope, w ticketWorld, svc store.Service,
	env store.Environment, app, note string,
) (store.Ticket, error) {
	tr := tickets.For(ch.Type, ticketHTTP)
	if tr == nil {
		return store.Ticket{}, fmt.Errorf("no tracker for %q", ch.Type)
	}
	u := w.update(svc.ID, env.ID, app)
	if u == nil || u.Acked {
		return store.Ticket{}, ErrNoUpgrade
	}
	app = u.App
	t, err := n.store.OpenTicket(ctx, sc, ch.ID, svc.ID, env.ID, app)
	if err == nil {
		if note != "" && note != t.LastNote {
			if err := tr.Comment(ctx, ch.Config, tickets.Ref{ID: t.ExternalID, Key: t.ExternalKey, URL: t.URL}, note); err != nil {
				return t, err
			}
			_ = n.store.NoteTicket(ctx, sc, t.ID, note)
		}
		return t, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return t, err
	}
	d := n.ticketDraft(*u, note)
	ref, err := tr.Create(ctx, ch.Config, d)
	if err != nil {
		return store.Ticket{}, err
	}
	t, err = n.store.CreateTicket(ctx, store.Ticket{
		Scope: sc, ChannelID: ch.ID, ServiceID: svc.ID, EnvironmentID: env.ID, App: app,
		ExternalID: ref.ID, ExternalKey: ref.Key, URL: ref.URL, Title: d.Title, LastNote: note,
	})
	if err == nil {
		n.log.Info("ticket filed", "channel", ch.Name, "ticket", ref.Key, "service", svc.Name, "environment", env.Name)
	}
	return t, err
}

// ticketDraft writes a ticket for an upgrade: what runs, what to move to, why and by when.
func (n *Notifier) ticketDraft(u versions.Update, note string) tickets.Draft {
	where := u.Service.Name + " @ " + u.Environment.Name
	if u.App != "" {
		where = u.Service.Name + " (" + u.App + ") @ " + u.Environment.Name
	}
	d := tickets.Draft{Team: u.Service.Owner, Due: u.Due, Labels: []string{"goliash"}}
	if u.Target != "" {
		d.Title = fmt.Sprintf("[Goliash] %s: upgrade %s → %s", where, orDash(u.Running), u.Target)
	} else {
		d.Title = fmt.Sprintf("[Goliash] %s: %s reaches its end of life", where, orDash(u.Running))
	}
	var b []string
	add := func(format string, args ...any) { b = append(b, fmt.Sprintf(format, args...)) }
	add("%s runs %s in %s.", u.Service.Name, orDash(u.Running), u.Environment.Name)
	if u.Target != "" {
		line := "Upgrade to " + u.Target + "."
		if u.TargetURL != "" {
			line += " Release notes: " + u.TargetURL
		}
		add("%s", line)
	}
	if len(u.Exploited) > 0 {
		add("Exploited in the wild (CISA KEV), in the running image: %s.", strings.Join(u.Exploited, ", "))
		d.Labels = append(d.Labels, "security")
	}
	if u.SecurityFix != "" {
		add("The releases up to %s fix: %s.", u.Target, u.SecurityFix)
		d.Labels = append(d.Labels, "security")
	}
	if u.Critical > 0 {
		add("Other critical vulnerabilities in the running image: %d.", u.Critical)
	}
	if u.EOL != "" {
		if u.EOLPassed {
			add("Its release cycle reached its end of life on %s.", u.EOL)
		} else {
			add("Its release cycle reaches its end of life on %s.", u.EOL)
		}
		d.Labels = append(d.Labels, "end-of-life")
	}
	if !u.Due.IsZero() {
		add("Remediation deadline: %s (%s).", u.Due.Format("2 Jan 2006"), u.DueFor)
	}
	if len(u.Exposed) > 0 {
		add("Reachable from outside: %s.", strings.Join(u.Exposed, "; "))
	}
	if len(u.Maintenance) > 0 {
		add("Waiting at the platform: %s.", strings.Join(u.Maintenance, "; "))
	}
	for _, br := range u.Breaking {
		add("Breaking: %s", br)
	}
	if note != "" {
		add("Why now: %s", note)
	}
	if n.link != "" {
		add("In Goliash: %s/services/%s", n.link, url.PathEscape(u.Service.Name))
	}
	add("Goliash closes this ticket once the upgrade runs in %s.", u.Environment.Name)
	d.Body = strings.Join(b, "\n\n")

	switch u.Urgency {
	case versions.UrgencyExploited:
		d.Priority = 0
	case versions.UrgencySecurity, versions.UrgencyEOLPassed:
		d.Priority = 1
	case versions.UrgencyEOLSoon, versions.UrgencyMajor:
		d.Priority = 2
	default:
		d.Priority = 3
	}
	if len(u.Exposed) > 0 && d.Priority > 0 {
		d.Priority--
	}
	return d
}

// CloseTickets closes the open tickets whose upgrade is no longer to do: deployed, or put off as an accepted risk,
// saying which.
func (n *Notifier) CloseTickets(ctx context.Context) error {
	wss, err := n.store.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, ws := range wss {
		sc := ws.Scope()
		open, err := n.store.ListOpenTickets(ctx, sc)
		if err != nil || len(open) == 0 {
			continue
		}
		chans, err := n.store.ListChannels(ctx, sc)
		if err != nil {
			return err
		}
		byID := map[string]store.Channel{}
		for _, c := range chans {
			byID[c.ID] = c
		}
		w, err := n.ticketWorld(ctx, sc)
		if err != nil {
			return err
		}
		for _, t := range open {
			ch, ok := byID[t.ChannelID]
			tr := tickets.For(ch.Type, ticketHTTP)
			if !ok || tr == nil {
				continue
			}
			comment := ""
			switch u := w.update(t.ServiceID, t.EnvironmentID, t.App); {
			case u == nil:
				comment = "Done: " + w.running(t.ServiceID, t.EnvironmentID) + " (checked by Goliash " + n.now().Format("2 Jan 2006 15:04 UTC") + ")."
			case u.Acked:
				comment = "Put off in Goliash as an accepted risk; closing. Goliash files a new ticket if it comes up again."
			default:
				continue
			}
			ref := tickets.Ref{ID: t.ExternalID, Key: t.ExternalKey, URL: t.URL}
			if err := tr.Close(ctx, ch.Config, ref, comment); err != nil {
				n.log.Warn("closing a ticket failed", "ticket", t.ExternalKey, "err", err)
				continue
			}
			if err := n.store.CloseTicket(ctx, sc, t.ID); err != nil {
				return err
			}
			n.log.Info("ticket closed", "ticket", t.ExternalKey)
		}
	}
	return nil
}

// running says what a service runs in an environment now: "payments-api runs 1.6.0 in prod".
func (w ticketWorld) running(serviceID, envID string) string {
	svc := w.o.Services[serviceID].Name
	env := w.o.Envs[envID].Name
	for _, row := range w.o.Matrix.Rows {
		if row.Service.ID != serviceID {
			continue
		}
		for i, e := range w.o.Matrix.Environments {
			if e.ID != envID || i >= len(row.Cells) {
				continue
			}
			var tags []string
			for _, v := range row.Cells[i].Versions {
				tags = append(tags, v.Tag)
			}
			if len(tags) == 0 {
				return svc + " no longer runs in " + env
			}
			return svc + " runs " + strings.Join(tags, ", ") + " in " + env
		}
	}
	return svc + " no longer needs the upgrade in " + env
}
