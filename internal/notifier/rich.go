// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"strings"

	"github.com/pipozzz/goliash/internal/versions"
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
		if it.Note == "rollback" {
			return kind{"⏪", "#e5484d", "Rolled back"}
		}
		return kind{"🔁", "#3b6cf6", "Version changed"}
	case EventRollout:
		if it.Note == "down" {
			return kind{"🛑", "#e5484d", "Down"}
		}
		return kind{"⏳", "#ffb347", "Rollout stuck"}
	case EventLicense:
		return kind{"⚖️", "#e5484d", "License"}
	case EventUnexpectedSigner:
		return kind{"✍️", "#e5484d", "Unexpected signer"}
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
	case EventWatch:
		return kind{"👁️", "#e5484d", "Security watch"}
	case EventSecurityBrief:
		return kind{"🛡️", "#1b2a6b", "Security brief"}
	case "security_release":
		return kind{"🔒", "#e5484d", "Security release"}
	case EventDeadline:
		switch {
		case strings.HasPrefix(it.Note, "approve|"):
			return kind{"✍️", "#d9822b", "Accepted risk to approve"}
		case strings.HasPrefix(it.Note, "ends|"):
			return kind{"⏳", "#d9822b", "Accepted risk ends"}
		}
		if strings.HasPrefix(it.Note, "passed") {
			return kind{"⏰", "#e5484d", "Remediation deadline passed"}
		}
		return kind{"⏳", "#d9822b", "Remediation deadline near"}
	case "vulnerability":
		if strings.Contains(it.Note, "exploited") {
			return kind{"🚨", "#e5484d", "Exploited vulnerability"}
		}
		return kind{"🛡️", "#e5484d", "Critical vulnerability"}
	case "agent_stale":
		return kind{"🔌", "#e5484d", "Agent silent"}
	case "agent_back":
		return kind{"✅", "#30a46c", "Agent back"}
	case EventGoliashRelease:
		if it.Note == "security" {
			return kind{"🔒", "#e5484d", "Goliash security release"}
		}
		return kind{"🆕", "#1b2a6b", "Goliash release"}
	case EventPullRequest:
		if it.Note == "failing" {
			return kind{"❌", "#e5484d", "Pull request failing"}
		}
		if strings.HasPrefix(it.Note, "undeployed ") {
			return kind{"⏸️", "#d9822b", "Merged, not deployed"}
		}
		if strings.HasPrefix(it.Note, "waiting ") {
			return kind{"⏳", "#d9822b", "Pull request waiting"}
		}
		return kind{"📬", "#1b2a6b", "Pull request opened"}
	case EventGitOps:
		if it.Note == "out of sync" {
			return kind{"🔀", "#d9822b", versions.GitOpsToolName(it.Tool) + " out of sync"}
		}
		return kind{"🔀", "#e5484d", versions.GitOpsToolName(it.Tool) + " " + it.Note}
	case "posture":
		return kind{"🛡️", "#1b2a6b", "Security posture"}
	case EventImageGrew:
		return kind{"📦", "#d9822b", "Image grew"}
	case EventImageOverBudget:
		return kind{"📦", "#e5484d", "Image over budget"}
	case EventOutdatedBase:
		return kind{"🧱", "#d9822b", "Outdated base"}
	case EventEvidenceLost:
		return kind{"🔓", "#e5484d", "Evidence lost"}
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
	if it.Type == "posture" || it.Type == EventSecurityBrief {
		return base + "/security"
	}
	if it.Service == "" {
		return base + "/"
	}
	return base + "/?q=" + url.QueryEscape(it.Service)
}

// goliLink opens Goliash with Goli's panel and a question about a single alert ready to ask: a click away from
// an explanation, never asked (and paid for) by the click alone.
func goliLink(msg Message) string {
	if !msg.Goli || msg.Link == "" || len(msg.Items) != 1 || msg.plan() {
		return ""
	}
	q := goliQuestion(msg.Items[0])
	if q == "" {
		return ""
	}
	return msg.Link + "/?goli=" + url.QueryEscape(q)
}

// GoliQuestion is what to ask Goli about a message, for the bell: about its one alert, or which of an upgrade
// plan's updates to do first. Empty when there is nothing to ask.
func GoliQuestion(msg Message) string {
	if msg.plan() {
		return "Which of the upgrades in my upgrade plan should we do first, and why? Breaking changes and security fixes first."
	}
	if len(msg.Items) != 1 {
		return ""
	}
	return goliQuestion(msg.Items[0])
}

func goliQuestion(it Item) string {
	where := it.Service
	if it.Environment != "" {
		where += " in " + it.Environment
	}
	switch {
	case it.Type == "update" || it.Type == "new_release" && it.From != "" && it.To != "":
		return "Brief me on upgrading " + where + " from " + it.From + " to " + it.To + ": what breaks or needs migrating, what it fixes (security first), and what to check."
	case it.Type == "vulnerability":
		return "Explain this vulnerability alert for us, what fixes it and how urgent it is: " + it.Text
	case it.Type == "drift_detected":
		return "Explain this drift alert: why " + where + " is in this state, since when, and what to do first: " + it.Text
	case it.Type == EventImageOverBudget:
		return "Why is the " + it.Service + " image in " + it.Environment + " " + it.From + ", over its " + it.Note + "? What takes the space and how could it shrink?"
	case it.Type == EventOutdatedBase:
		return "The " + it.Service + " image in " + it.Environment + " is built on an older " + it.From + ". What fixes is it missing, and how do we rebuild it on the current base?"
	case it.Type == EventEvidenceLost:
		return "Version " + it.To + " of " + it.Service + " in " + it.Environment + " has no " + it.Note + ", though " + it.From + " had. What might have changed in the build, and what is the risk?"
	case it.Type == EventImageGrew:
		return "Why did the " + it.Service + " image in " + it.Environment + " grow from " + it.From + " to " + it.To + " (" + it.Note + ")? Brief me on what changed between the versions (base image, dependencies, build) and how the image could get smaller."
	case it.Type == "posture":
		return "Walk me through our security posture: what to fix first to cut production exposure, what is past its end of life, and which accepted risks to review."
	case it.Type == EventSecurityBrief:
		return "From today's security brief, what should we do first and why? " + it.Text
	case it.Type == "agent_stale":
		return "Which data may be stale because of this, and what should I check? " + it.Text
	}
	return ""
}

// openLink is the button under a whole message.
func openLink(msg Message) string {
	if msg.Link == "" {
		return ""
	}
	if msg.plan() {
		return msg.Link + "/updates"
	}
	if len(msg.Items) == 1 && msg.Items[0].Type == "posture" {
		return msg.Link + "/security"
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
			map[string]any{"type": "context", "elements": []any{text("mrkdwn", "Workspace *"+slackEscape(msg.Workspace)+"* · "+summary(msg))}})
		if msg.Take != "" {
			blocks = append(blocks, map[string]any{"type": "section", "text": text("mrkdwn", "*Goli's take:* "+slackEscape(truncate(msg.Take, 2900)))})
		}
		blocks = append(blocks, map[string]any{"type": "divider"})
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
		if it.From != "" && it.To != "" && it.Type != "drift_detected" && it.Type != EventGitOps || it.Type == "update" {
			body += "\n`" + slackEscape(orDash(it.From)) + "` → `" + slackEscape(orDash(it.To)) + "`"
		}
		if a := actionURL(msg.Link, it); a != "" && !single { // the single item's goes in its buttons
			body += " · <" + a + "|" + slackEscape(it.Action) + ">"
		}
		sec := map[string]any{"type": "section", "text": text("mrkdwn", truncate(body, 3000))}
		if it.URL != "" {
			sec["accessory"] = button(linkLabel(it), it.URL, false)
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
		actions := []any{button(label, u, true)}
		if single {
			if a := actionURL(msg.Link, msg.Items[0]); a != "" {
				actions = append(actions, button(msg.Items[0].Action, a, false))
			}
		}
		if g := goliLink(msg); g != "" {
			actions = append(actions, button("Ask Goli", g, false))
		}
		blocks = append(blocks, map[string]any{"type": "actions", "elements": actions})
	}
	return blocks
}

// summary counts a message's items by kind: "2 new releases · 1 drift".
func summary(msg Message) string {
	if len(msg.Items) == 1 && msg.Items[0].Type == "posture" {
		return "production"
	}
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
			fields = append(fields, map[string]any{"name": linkLabel(it), "value": "[open](" + it.URL + ")", "inline": true})
		}
		if a := actionURL(msg.Link, it); a != "" {
			fields = append(fields, map[string]any{"name": it.Action, "value": "[preview](" + a + ")", "inline": true})
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
		NotesLabel         string
		When, Target, Team string
	}
	data := struct {
		Title, Summary, Workspace, Open, OpenLabel, Take, Goli string
		Rows                                                   []row
	}{Title: msg.Title(), Summary: summary(msg), Workspace: msg.Workspace, Open: openLink(msg), OpenLabel: "Open in Goliash", Take: msg.Take, Goli: goliLink(msg)}
	if msg.plan() {
		data.OpenLabel = "Open the Updates page"
	}
	for _, it := range msg.Items {
		data.Rows = append(data.Rows, row{
			Kind: kindOf(it), Where: where(it), What: what(it), From: it.From, To: it.To,
			Arrow: it.From != "" && it.To != "" && it.Type != "drift_detected" && it.Type != EventGitOps || it.Type == "update",
			Notes: it.URL, NotesLabel: strings.ToLower(linkLabel(it)), Open: itemLink(msg.Link, it), When: it.At.UTC().Format("2 Jan 15:04 UTC"),
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
{{if .Take}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin-top:16px;background:#eef1ff;border-radius:10px"><tr><td style="padding:12px 16px;font-size:14px;line-height:1.5;color:#1c2333"><b style="color:#1b2a6b">Goli's take:</b> {{.Take}}</td></tr></table>{{end}}
{{range .Rows}}
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin-top:16px;border-left:4px solid {{.Kind.Colour}};background:#f8f9fc;border-radius:0 10px 10px 0">
  <tr><td style="padding:12px 16px">
    <div style="font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.06em;color:#6d7688">{{.Kind.Emoji}} {{.Kind.Label}}</div>
    {{if .Where}}<div style="font-size:15px;font-weight:700;margin-top:4px">{{if .Open}}<a href="{{.Open}}" style="color:#1c2333;text-decoration:none">{{.Where}}</a>{{else}}{{.Where}}{{end}}</div>{{end}}
    <div style="font-size:14px;color:#3d4659;margin-top:2px">{{.What}}</div>
    {{if .Arrow}}<div style="margin-top:8px;font-family:SFMono-Regular,Menlo,Consolas,monospace;font-size:13px"><span style="background:#eceef3;border-radius:5px;padding:2px 7px">{{if .From}}{{.From}}{{else}}—{{end}}</span> <span style="color:#8a93a6">→</span> <span style="background:#e3e9ff;color:#1b2a6b;border-radius:5px;padding:2px 7px;font-weight:600">{{if .To}}{{.To}}{{else}}—{{end}}</span></div>{{end}}
    <div style="font-size:12px;color:#8a93a6;margin-top:8px">{{.When}}{{if .Target}} · on {{.Target}}{{end}}{{if .Team}} · {{.Team}}{{end}}{{if .Notes}} · <a href="{{.Notes}}" style="color:#3b6cf6">{{.NotesLabel}}</a>{{end}}</div>
  </td></tr></table>
{{end}}
{{if .Open}}<table role="presentation" cellpadding="0" cellspacing="0" style="margin-top:22px"><tr><td style="background:#1b2a6b;border-radius:9px"><a href="{{.Open}}" style="display:inline-block;padding:11px 20px;color:#ffffff;font-weight:600;font-size:14px;text-decoration:none">{{.OpenLabel}} →</a></td>{{if .Goli}}<td width="10"></td><td style="background:#e3e9ff;border-radius:9px"><a href="{{.Goli}}" style="display:inline-block;padding:11px 20px;color:#1b2a6b;font-weight:600;font-size:14px;text-decoration:none">Ask Goli</a></td>{{end}}</tr></table>{{end}}
</td></tr>
<tr><td style="padding:16px 8px;text-align:center;font-size:12px;color:#8a93a6">Sent by Goliash because a notification rule matched. Change rules on the Notifications page.</td></tr>
</table></td></tr></table>
</body></html>
`))
