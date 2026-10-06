// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"strings"
)

// Rich messages: Slack blocks, Discord embeds and HTML e-mail share how an item looks,
// its kind (an emoji, a colour, a label), where it happened and what changed.

// kind is how an item type is shown.
type kind struct {
	Emoji, Colour, Label string
}

func kindOf(it Item) kind {
	switch it.Type {
	case "deployed":
		return kind{"🚀", "#3b6cf6", "Deployed"}
	case "version_changed":
		if it.Note == "retag" {
			return kind{"♻️", "#ffb347", "Re-pushed"}
		}
		return kind{"🔁", "#3b6cf6", "Version changed"}
	case "removed":
		return kind{"🗑️", "#8a93a6", "Removed"}
	case "new_release":
		return kind{"✨", "#8aa4ff", "New release"}
	case "drift_detected":
		if it.Note == "eol" {
			return kind{"⛔", "#e5484d", "End of life"}
		}
		return kind{"⚠️", "#ffb347", "Drift"}
	case "drift_resolved":
		return kind{"✅", "#30a46c", "Drift resolved"}
	case "update":
		return kind{"⬆️", urgencyColour(it.Note), "Update · " + it.Note}
	case "agent_stale":
		return kind{"🔌", "#e5484d", "Agent silent"}
	case "agent_back":
		return kind{"✅", "#30a46c", "Agent back"}
	}
	return kind{"👋", "#1b2a6b", "Goliash"}
}

func urgencyColour(urgency string) string {
	switch urgency {
	case "end of life", "end of life soon":
		return "#e5484d"
	case "major", "minor", "behind previous env":
		return "#ffb347"
	}
	return "#8aa4ff"
}

// where names the service, its application and environment.
func where(it Item) string {
	parts := []string{}
	if it.Service != "" {
		parts = append(parts, it.Service)
	}
	if it.App != "" {
		parts = append(parts, it.App)
	}
	if it.Environment != "" {
		parts = append(parts, it.Environment)
	}
	return strings.Join(parts, " · ")
}

// what is the item's text without the "service @ env: " prefix where says already.
func what(it Item) string {
	if w := where(it); w != "" {
		if _, rest, ok := strings.Cut(it.Text, ": "); ok {
			return rest
		}
	}
	return it.Text
}

// itemLink opens the item in Goliash: the matrix filtered to its service.
func itemLink(base string, it Item) string {
	if base == "" {
		return ""
	}
	if it.Service == "" {
		return base + "/"
	}
	return base + "/?q=" + url.QueryEscape(it.Service)
}

// openLink is the button under a whole message.
func openLink(msg Message) string {
	if msg.Link == "" {
		return ""
	}
	if msg.plan() {
		return msg.Link + "/updates"
	}
	return msg.Link + "/"
}

// slackEscape escapes the three characters Slack's mrkdwn treats as markup.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// slackItems is the most items one Slack message lists (a message holds 50 blocks).
const slackItems = 20

func slackBlocks(msg Message) []map[string]any {
	text := func(t, s string) map[string]any { return map[string]any{"type": t, "text": s} }
	button := func(label, u string, primary bool) map[string]any {
		b := map[string]any{"type": "button", "text": map[string]any{"type": "plain_text", "text": label, "emoji": true}, "url": u}
		if primary {
			b["style"] = "primary"
		}
		return b
	}
	var blocks []map[string]any
	single := len(msg.Items) == 1 && !msg.Digest
	if !single {
		blocks = append(blocks,
			map[string]any{"type": "header", "text": map[string]any{"type": "plain_text", "text": truncate(msg.Title(), 150), "emoji": true}},
			map[string]any{"type": "context", "elements": []any{text("mrkdwn", "Workspace *"+slackEscape(msg.Workspace)+"* · "+summary(msg))}},
			map[string]any{"type": "divider"})
	}
	for i, it := range msg.Items {
		if i == slackItems {
			blocks = append(blocks, map[string]any{"type": "context", "elements": []any{text("mrkdwn", fmt.Sprintf("…and %d more in Goliash", len(msg.Items)-slackItems))}})
			break
		}
		k := kindOf(it)
		head := k.Emoji + " *" + slackEscape(orDash(where(it))) + "*"
		if where(it) == "" {
			head = k.Emoji + " *" + k.Label + "*"
		}
		body := head + "\n" + slackEscape(what(it))
		if it.From != "" && it.To != "" && it.Type != "drift_detected" || it.Type == "update" {
			body += "\n`" + slackEscape(orDash(it.From)) + "` → `" + slackEscape(orDash(it.To)) + "`"
		}
		sec := map[string]any{"type": "section", "text": text("mrkdwn", truncate(body, 3000))}
		if it.URL != "" {
			sec["accessory"] = button("Release notes", it.URL, false)
		} else if u := itemLink(msg.Link, it); u != "" && !single {
			sec["accessory"] = button("Open", u, false)
		}
		blocks = append(blocks, sec)
		meta := k.Label
		if it.Target != "" {
			meta += " · on " + it.Target
		}
		if it.Owner != "" {
			meta += " · " + it.Owner
		}
		blocks = append(blocks, map[string]any{"type": "context", "elements": []any{text("mrkdwn", slackEscape(meta))}})
	}
	if u := openLink(msg); u != "" {
		label := "Open in Goliash"
		if msg.plan() {
			label = "Open the Updates page"
		} else if single {
			if l := itemLink(msg.Link, msg.Items[0]); l != "" {
				u = l
			}
		}
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []any{button(label, u, true)}})
	}
	return blocks
}

// summary counts a message's items by kind: "2 new releases · 1 drift".
func summary(msg Message) string {
	var order []string
	counts := map[string]int{}
	for _, it := range msg.Items {
		l := kindOf(it).Label
		if msg.plan() {
			l = it.Note // by urgency: "1 end of life · 2 minor"
		}
		if counts[l] == 0 {
			order = append(order, l)
		}
		counts[l]++
	}
	out := make([]string, len(order))
	for i, l := range order {
		out[i] = fmt.Sprintf("%d %s", counts[l], strings.ToLower(l))
	}
	return strings.Join(out, " · ")
}

// discordEmbeds is one embed per item, coloured by kind (Discord takes ten).
func discordEmbeds(msg Message) []map[string]any {
	var out []map[string]any
	for i, it := range msg.Items {
		if i == 10 {
			break
		}
		k := kindOf(it)
		var colour int
		_, _ = fmt.Sscanf(k.Colour, "#%x", &colour)
		title := k.Emoji + " " + orDash(where(it))
		if where(it) == "" {
			title = k.Emoji + " " + k.Label
		}
		e := map[string]any{"title": truncate(title, 256), "description": truncate(what(it), 4096), "color": colour}
		if u := itemLink(msg.Link, it); u != "" {
			e["url"] = u
		}
		var fields []map[string]any
		if it.From != "" {
			fields = append(fields, map[string]any{"name": "Running", "value": "`" + it.From + "`", "inline": true})
		}
		if it.To != "" && it.Type != "drift_detected" || it.Type == "update" && it.To != "" {
			fields = append(fields, map[string]any{"name": "New", "value": "`" + it.To + "`", "inline": true})
		}
		if it.URL != "" {
			fields = append(fields, map[string]any{"name": "Release notes", "value": "[open](" + it.URL + ")", "inline": true})
		}
		if fields != nil {
			e["fields"] = fields
		}
		e["footer"] = map[string]any{"text": k.Label + " · " + msg.Workspace}
		e["timestamp"] = it.At.UTC().Format("2006-01-02T15:04:05Z")
		out = append(out, e)
	}
	return out
}

// emailHTML renders a message as an HTML e-mail: inline styles only, a table layout,
// the logo's navy and periwinkle; mail clients drop <style> and scripts.
func emailHTML(msg Message) (string, error) {
	type row struct {
		Kind               kind
		Where, What        string
		From, To           string
		Arrow              bool
		Notes, Open        string
		When, Target, Team string
	}
	data := struct {
		Title, Summary, Workspace, Open, OpenLabel string
		Rows                                       []row
	}{Title: msg.Title(), Summary: summary(msg), Workspace: msg.Workspace, Open: openLink(msg), OpenLabel: "Open in Goliash"}
	if msg.plan() {
		data.OpenLabel = "Open the Updates page"
	}
	for _, it := range msg.Items {
		data.Rows = append(data.Rows, row{
			Kind: kindOf(it), Where: where(it), What: what(it), From: it.From, To: it.To,
			Arrow: it.From != "" && it.To != "" && it.Type != "drift_detected" || it.Type == "update",
			Notes: it.URL, Open: itemLink(msg.Link, it), When: it.At.UTC().Format("2 Jan 15:04 UTC"),
			Target: it.Target, Team: it.Owner,
		})
	}
	var b bytes.Buffer
	err := emailTemplate.Execute(&b, data)
	return b.String(), err
}

var emailTemplate = template.Must(template.New("mail").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>{{.Title}}</title></head>
<body style="margin:0;padding:0;background:#f4f5f7;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#1c2333">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f4f5f7;padding:24px 12px"><tr><td align="center">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:600px">
<tr><td style="background:#1b2a6b;border-radius:14px 14px 0 0;padding:20px 24px">
  <table role="presentation" cellpadding="0" cellspacing="0"><tr>
    <td style="padding-right:12px"><table role="presentation" cellpadding="0" cellspacing="3"><tr><td width="10" height="10" style="background:#8aa4ff;border-radius:3px"></td><td width="10" height="10" style="background:#8aa4ff;border-radius:3px;opacity:.55"></td></tr><tr><td width="10" height="10" style="background:#8aa4ff;border-radius:3px;opacity:.55"></td><td width="10" height="10" style="background:#ffb347;border-radius:3px"></td></tr></table></td>
    <td style="color:#ffffff;font-weight:700;font-size:16px;letter-spacing:.02em">Goliash</td>
    <td style="color:#aab6e8;font-size:13px;padding-left:10px">{{.Workspace}}</td>
  </tr></table>
  <div style="color:#ffffff;font-size:20px;font-weight:700;line-height:1.3;margin-top:14px">{{.Title}}</div>
  {{if .Summary}}<div style="color:#aab6e8;font-size:13px;margin-top:4px">{{.Summary}}</div>{{end}}
</td></tr>
<tr><td style="background:#ffffff;border-radius:0 0 14px 14px;padding:8px 24px 24px">
{{range .Rows}}
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin-top:16px;border-left:4px solid {{.Kind.Colour}};background:#f8f9fc;border-radius:0 10px 10px 0">
  <tr><td style="padding:12px 16px">
    <div style="font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.06em;color:#6d7688">{{.Kind.Emoji}} {{.Kind.Label}}</div>
    {{if .Where}}<div style="font-size:15px;font-weight:700;margin-top:4px">{{if .Open}}<a href="{{.Open}}" style="color:#1c2333;text-decoration:none">{{.Where}}</a>{{else}}{{.Where}}{{end}}</div>{{end}}
    <div style="font-size:14px;color:#3d4659;margin-top:2px">{{.What}}</div>
    {{if .Arrow}}<div style="margin-top:8px;font-family:SFMono-Regular,Menlo,Consolas,monospace;font-size:13px"><span style="background:#eceef3;border-radius:5px;padding:2px 7px">{{if .From}}{{.From}}{{else}}—{{end}}</span> <span style="color:#8a93a6">→</span> <span style="background:#e3e9ff;color:#1b2a6b;border-radius:5px;padding:2px 7px;font-weight:600">{{if .To}}{{.To}}{{else}}—{{end}}</span></div>{{end}}
    <div style="font-size:12px;color:#8a93a6;margin-top:8px">{{.When}}{{if .Target}} · on {{.Target}}{{end}}{{if .Team}} · {{.Team}}{{end}}{{if .Notes}} · <a href="{{.Notes}}" style="color:#3b6cf6">release notes</a>{{end}}</div>
  </td></tr></table>
{{end}}
{{if .Open}}<table role="presentation" cellpadding="0" cellspacing="0" style="margin-top:22px"><tr><td style="background:#1b2a6b;border-radius:9px"><a href="{{.Open}}" style="display:inline-block;padding:11px 20px;color:#ffffff;font-weight:600;font-size:14px;text-decoration:none">{{.OpenLabel}} →</a></td></tr></table>{{end}}
</td></tr>
<tr><td style="padding:16px 8px;text-align:center;font-size:12px;color:#8a93a6">Sent by Goliash because a notification rule matched. Change rules on the Notifications page.</td></tr>
</table></td></tr></table>
</body></html>
`))
