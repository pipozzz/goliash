// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/pipozzz/goliash/internal/store"
)

// pagerDutyEvents is PagerDuty's Events API v2.
const pagerDutyEvents = "https://events.pagerduty.com/v2/enqueue"

// opsgenieAPI is Opsgenie's API in the US; EU accounts use https://api.eu.opsgenie.com.
const opsgenieAPI = "https://api.opsgenie.com"

// alertKey is what makes repeats of one alert the same incident, and its all clear the one that closes it: an
// agent's silence and its return share a key, as do a drift and its resolution. Resolves says the item closes the
// incident under its key rather than opening one.
func alertKey(workspace string, it Item) (key string, resolves bool) {
	typ := it.Type
	switch it.Type {
	case "agent_back":
		typ, resolves = "agent_stale", true
	case "drift_resolved":
		typ, resolves = "drift_detected", true
	}
	parts := []string{"goliash", workspace, typ, it.Service, it.App, it.Environment, it.Target, it.Workload}
	switch typ {
	case "agent_stale", "drift_detected", EventRollout, EventGitOps:
		parts = append(parts, it.Note) // the kind of drift or of rollout trouble
	default:
		parts = append(parts, it.To, it.Note) // a vulnerability, a release: one incident each
	}
	return strings.Join(parts, "|"), resolves
}

// alertSeverity is how loud an alert is: PagerDuty's critical, error, warning or info.
func alertSeverity(it Item) string {
	switch it.Type {
	case "vulnerability", EventDeadline:
		if it.Production {
			return "critical"
		}
		return "error"
	case "agent_stale", EventRollout, EventGitOps:
		return "error"
	case "drift_detected", "security_release", EventWatch:
		return "warning"
	}
	return "info"
}

// oncallItems are the items an incident tool takes: alerts, not reports. An upgrade plan, a posture report or a
// security brief is a page to read, not something to page someone for.
func oncallItems(msg Message) []Item {
	if msg.plan() {
		return nil
	}
	var out []Item
	for _, it := range msg.Items {
		if it.Type == "posture" || it.Type == EventSecurityBrief {
			continue
		}
		out = append(out, it)
	}
	return out
}

// PagerDuty sends each alert to a PagerDuty service through its Events API v2 integration (the routing key):
// repeats join one incident, and an agent back or a drift resolved resolves it.
type PagerDuty struct {
	HTTP *http.Client
	URL  string // the Events API, for tests
}

// Send implements Sender.
func (p PagerDuty) Send(ctx context.Context, ch store.Channel, msg Message) error {
	var cfg struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(ch.Config, &cfg); err != nil || cfg.Token == "" {
		return fmt.Errorf("channel %s needs the integration key of a PagerDuty service (Events API v2)", ch.Name)
	}
	endpoint := p.URL
	if endpoint == "" {
		endpoint = pagerDutyEvents
	}
	for _, it := range oncallItems(msg) {
		key, resolves := alertKey(msg.Workspace, it)
		ev := map[string]any{"routing_key": cfg.Token, "dedup_key": key, "event_action": "trigger"}
		if resolves {
			ev["event_action"] = "resolve"
		} else {
			details := map[string]string{}
			for k, v := range map[string]string{
				"service": it.Service, "environment": it.Environment, "owner": it.Owner,
				"running": it.From, "to": it.To, "target": it.Target, "note": it.Note,
			} {
				if v != "" {
					details[k] = v
				}
			}
			ev["payload"] = map[string]any{
				"summary": truncate(it.Text, 1024), "source": orDefault(it.Target, orDefault(it.Service, "goliash")),
				"severity": alertSeverity(it), "timestamp": it.At.UTC().Format("2006-01-02T15:04:05Z"),
				"component": it.Service, "group": it.Environment, "class": it.Type, "custom_details": details,
			}
			var links []map[string]string
			for _, l := range alertLinks(msg, it) {
				links = append(links, map[string]string{"href": l.Href, "text": l.Text})
			}
			if links != nil {
				ev["links"] = links
			}
		}
		body, _ := json.Marshal(ev)
		if err := post(ctx, p.HTTP, endpoint, body, nil); err != nil {
			return fmt.Errorf("pagerduty: %w", err)
		}
	}
	return nil
}

// Opsgenie opens an Opsgenie alert for each alert with an API integration's key: repeats join one alert by its
// alias, and an agent back or a drift resolved closes it. Config: token, and url for EU accounts.
type Opsgenie struct{ HTTP *http.Client }

// Send implements Sender.
func (o Opsgenie) Send(ctx context.Context, ch store.Channel, msg Message) error {
	var cfg struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(ch.Config, &cfg); err != nil || cfg.Token == "" {
		return fmt.Errorf("channel %s needs the API key of an Opsgenie API integration", ch.Name)
	}
	base := strings.TrimSuffix(orDefault(cfg.URL, opsgenieAPI), "/")
	headers := map[string]string{"Authorization": "GenieKey " + cfg.Token}
	for _, it := range oncallItems(msg) {
		key, resolves := alertKey(msg.Workspace, it)
		alias := opsgenieAlias(key)
		if resolves {
			body, _ := json.Marshal(map[string]string{"source": "Goliash", "note": it.Text})
			if err := post(ctx, o.HTTP, base+"/v2/alerts/"+url.PathEscape(alias)+"/close?identifierType=alias", body, headers); err != nil {
				return fmt.Errorf("opsgenie: %w", err)
			}
			continue
		}
		var tags []string
		for _, t := range []string{"goliash", it.Type, it.Service, it.Environment, it.Owner} {
			if t != "" {
				tags = append(tags, t)
			}
		}
		desc := it.Text
		for _, l := range alertLinks(msg, it) {
			desc += "\n" + l.Text + ": " + l.Href
		}
		body, _ := json.Marshal(map[string]any{
			"message": truncate(it.Text, 130), "alias": alias, "description": truncate(desc, 15000), "source": "Goliash",
			"priority": opsgeniePriority(alertSeverity(it)), "tags": tags, "entity": it.Service,
			"details": map[string]string{"service": it.Service, "environment": it.Environment, "type": it.Type},
		})
		if err := post(ctx, o.HTTP, base+"/v2/alerts", body, headers); err != nil {
			return fmt.Errorf("opsgenie: %w", err)
		}
	}
	return nil
}

// opsgenieAlias fits an alert key in Opsgenie's 512 characters.
func opsgenieAlias(key string) string {
	if len(key) <= 512 {
		return key
	}
	return key[:512]
}

func opsgeniePriority(severity string) string {
	switch severity {
	case "critical":
		return "P1"
	case "error":
		return "P2"
	case "warning":
		return "P3"
	}
	return "P5"
}

// alertLink is a link an alert carries: label and address.
type alertLink struct{ Text, Href string }

// alertLinks are where an alert leads: Goliash, and its own link (release notes, a commit, a pull request).
func alertLinks(msg Message, it Item) []alertLink {
	var out []alertLink
	if l := itemLink(msg.Link, it); l != "" {
		out = append(out, alertLink{"Open in Goliash", l})
	}
	if it.URL != "" {
		out = append(out, alertLink{linkLabel(it), it.URL})
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
