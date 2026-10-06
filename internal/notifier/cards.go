// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"strconv"

	"github.com/pipozzz/goliash/internal/store"
)

// cardItems is the most items one Teams or Google Chat card lists.
const cardItems = 20

// arrow says what changed in versions, or "" when nothing to show.
func arrow(it Item) string {
	if it.Type == "update" || it.From != "" && it.To != "" && it.Type != "drift_detected" {
		return orDash(it.From) + " → " + orDash(it.To)
	}
	return ""
}

// Teams posts an Adaptive Card to a Microsoft Teams channel through a Workflows
// webhook ("Post to a channel when a webhook request is received").
type Teams struct{ HTTP *http.Client }

// Send implements Sender.
func (t Teams) Send(ctx context.Context, ch store.Channel, msg Message) error {
	u, _, err := channelURL(ch)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(teamsCard(msg))
	return post(ctx, t.HTTP, u, body, nil)
}

// teamsStyle maps a kind's colour to the container styles Teams has.
func teamsStyle(it Item) string {
	switch kindOf(it).Colour {
	case "#e5484d":
		return "attention"
	case "#ffb347":
		return "warning"
	case "#30a46c":
		return "good"
	}
	return "emphasis"
}

func teamsCard(msg Message) map[string]any {
	text := func(s string, extra map[string]any) map[string]any {
		b := map[string]any{"type": "TextBlock", "text": s, "wrap": true}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	body := []any{text(msg.Title(), map[string]any{"weight": "Bolder", "size": "Medium"})}
	if msg.Digest || len(msg.Items) > 1 {
		body = append(body, text("Workspace "+msg.Workspace+" · "+summary(msg), map[string]any{"isSubtle": true, "spacing": "None"}))
	}
	for i, it := range msg.Items {
		if i == cardItems {
			body = append(body, text("…and "+strconv.Itoa(len(msg.Items)-cardItems)+" more in Goliash", map[string]any{"isSubtle": true}))
			break
		}
		k := kindOf(it)
		head := k.Emoji + " **" + orDash(where(it)) + "**"
		if where(it) == "" {
			head = k.Emoji + " **" + k.Label + "**"
		}
		items := []any{
			text(head, nil),
			text(what(it), map[string]any{"spacing": "None"}),
		}
		var facts []any
		if a := arrow(it); a != "" {
			facts = append(facts, map[string]any{"title": "Version", "value": a})
		}
		if it.Target != "" {
			facts = append(facts, map[string]any{"title": "Target", "value": it.Target})
		}
		if it.Owner != "" {
			facts = append(facts, map[string]any{"title": "Team", "value": it.Owner})
		}
		if facts != nil {
			items = append(items, map[string]any{"type": "FactSet", "facts": facts, "spacing": "Small"})
		}
		c := map[string]any{"type": "Container", "style": teamsStyle(it), "items": items, "spacing": "Medium", "bleed": false}
		if u := itemLink(msg.Link, it); u != "" {
			c["selectAction"] = map[string]any{"type": "Action.OpenUrl", "url": u}
		}
		if it.URL != "" {
			items = append(items, map[string]any{"type": "ActionSet", "actions": []any{map[string]any{"type": "Action.OpenUrl", "title": "Release notes", "url": it.URL}}})
			c["items"] = items
		}
		body = append(body, c)
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard", "version": "1.4", "body": body,
		"msteams": map[string]any{"width": "Full"},
	}
	if u := openLink(msg); u != "" {
		title := "Open in Goliash"
		if msg.plan() {
			title = "Open the Updates page"
		}
		card["actions"] = []any{map[string]any{"type": "Action.OpenUrl", "title": title, "url": u}}
	}
	return map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"contentUrl":  nil,
			"content":     card,
		}},
	}
}

// GoogleChat posts a card to a Google Chat space through an incoming webhook.
type GoogleChat struct{ HTTP *http.Client }

// Send implements Sender.
func (g GoogleChat) Send(ctx context.Context, ch store.Channel, msg Message) error {
	u, _, err := channelURL(ch)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(googleChatCard(msg))
	return post(ctx, g.HTTP, u, body, nil)
}

func googleChatCard(msg Message) map[string]any {
	button := func(label, u string) map[string]any {
		return map[string]any{"text": label, "onClick": map[string]any{"openLink": map[string]any{"url": u}}}
	}
	var widgets []any
	for i, it := range msg.Items {
		if i == cardItems {
			widgets = append(widgets, map[string]any{"textParagraph": map[string]any{"text": "…and " + strconv.Itoa(len(msg.Items)-cardItems) + " more in Goliash"}})
			break
		}
		k := kindOf(it)
		title := orDash(where(it))
		if where(it) == "" {
			title = k.Label
		}
		// Google Chat reads a little HTML here; everything from outside is escaped.
		text := `<b>` + html.EscapeString(title) + `</b><br>` + html.EscapeString(what(it))
		if a := arrow(it); a != "" {
			text += `<br><font color="` + k.Colour + `">` + html.EscapeString(a) + `</font>`
		}
		d := map[string]any{"topLabel": k.Emoji + " " + k.Label, "text": text, "wrapText": true}
		if it.URL != "" {
			d["button"] = button("Release notes", it.URL)
		} else if u := itemLink(msg.Link, it); u != "" {
			d["button"] = button("Open", u)
		}
		widgets = append(widgets, map[string]any{"decoratedText": d})
	}
	sections := []any{map[string]any{"widgets": widgets}}
	if u := openLink(msg); u != "" {
		label := "Open in Goliash"
		if msg.plan() {
			label = "Open the Updates page"
		}
		sections = append(sections, map[string]any{"widgets": []any{map[string]any{"buttonList": map[string]any{"buttons": []any{button(label, u)}}}}})
	}
	header := map[string]any{"title": truncate(msg.Title(), 200)}
	if msg.Digest || len(msg.Items) > 1 {
		header["subtitle"] = "Workspace " + msg.Workspace + " · " + summary(msg)
	}
	if msg.Link != "" {
		header["imageUrl"], header["imageType"] = msg.Link+"/static/icon-192.png", "SQUARE"
	}
	return map[string]any{
		"text":    msg.Title(), // the notification's preview
		"cardsV2": []any{map[string]any{"cardId": "goliash", "card": map[string]any{"header": header, "sections": sections}}},
	}
}
