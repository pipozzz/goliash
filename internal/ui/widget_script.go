// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"strings"
)

// scriptableWidget is a script for Scriptable (iPhone, iPad): a home or lock screen widget with Goliash's numbers
// from /api/v1/summary, in every widget size; tapping it opens the Security page.
func scriptableWidget(serverURL, token string) string {
	if serverURL == "" {
		serverURL = "https://goliash.example.com"
	}
	if token == "" {
		token = "glsh_api_…" //nolint:gosec // a placeholder for the reader to replace
	}
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return strings.NewReplacer("$SERVER", q(strings.TrimRight(serverURL, "/")), "$TOKEN", q(token)).Replace(scriptableSource)
}

const scriptableSource = `// Goliash widget for Scriptable (iPhone, iPad). Add a Scriptable widget, then pick this script.
const SERVER = $SERVER
const TOKEN = $TOKEN // a viewer token: it can only read

const navy = new Color("#1b2a6b"), soft = new Color("#8aa4ff"), white = Color.white()
const amber = new Color("#ffb347"), red = new Color("#ff6b7a")

async function load() {
  const req = new Request(SERVER + "/api/v1/summary")
  req.headers = { Authorization: "Bearer " + TOKEN }
  req.timeoutInterval = 20
  try {
    const s = await req.loadJSON()
    if (req.response.statusCode !== 200) return { error: s.title || "HTTP " + req.response.statusCode }
    return s
  } catch (e) {
    return { error: "Goliash cannot be reached" }
  }
}

function tile(stack, value, label, colour) {
  const t = stack.addStack()
  t.layoutVertically()
  const v = t.addText(String(value))
  v.font = Font.boldRoundedSystemFont(20)
  v.textColor = colour
  const l = t.addText(label)
  l.font = Font.systemFont(10)
  l.textColor = soft
  l.lineLimit = 1
}

function row(w, tiles) {
  const r = w.addStack()
  tiles.forEach((t, i) => {
    if (i > 0) r.addSpacer()
    tile(r, t[0], t[1], t[2])
  })
  r.addSpacer()
}

const s = await load()
const family = config.widgetFamily || "medium"
const w = new ListWidget()
w.backgroundColor = navy
w.url = SERVER + "/security"
w.refreshAfterDate = new Date(Date.now() + 15 * 60 * 1000)

if (family.startsWith("accessory")) {
  // Lock screen: one line.
  const t = w.addText(s.error ? "Goliash: " + s.error : s.behind + " behind · " + s.median_exposure_days + "d · " + s.drifting + " drift")
  t.font = Font.mediumSystemFont(12)
  t.lineLimit = 2
} else {
  const head = w.addText("Goliash" + (s.workspace ? " · " + s.workspace : ""))
  head.font = Font.semiboldSystemFont(12)
  head.textColor = soft
  head.lineLimit = 1
  w.addSpacer(8)
  if (s.error) {
    const e = w.addText(s.error)
    e.textColor = red
    e.font = Font.systemFont(13)
  } else {
    const warn = (n, c) => (n > 0 ? c : white)
    const exposure = [s.median_exposure_days + "d", "median behind", warn(s.median_exposure_days > 30 ? 1 : 0, amber)]
    const eol = [s.end_of_life_passed, "end of life", warn(s.end_of_life_passed, red)]
    if (family === "small") {
      row(w, [exposure])
      w.addSpacer(6)
      row(w, [[s.drifting, "drifting", warn(s.drifting, amber)], eol])
    } else {
      row(w, [[s.drifting, "drifting", warn(s.drifting, amber)], [s.updates, "updates", white], [s.exploited, "exploited", warn(s.exploited, red)]])
      w.addSpacer(8)
      row(w, [exposure, [s.behind_90_days, "90+ days", warn(s.behind_90_days, amber)], eol])
      if (family === "large" || family === "extraLarge") {
        w.addSpacer(8)
        row(w, [[s.services, "services", white], [s.behind, "behind in " + s.production, warn(s.behind, amber)], [s.stale_targets, "stale targets", warn(s.stale_targets, amber)]])
      }
    }
    w.addSpacer()
    const at = w.addText(s.generated)
    at.font = Font.systemFont(9)
    at.textColor = soft
  }
}

if (config.runsInWidget) Script.setWidget(w)
else await w.presentMedium()
Script.complete()
`

// menuBarPlugin is a SwiftBar or xbar plugin: Goliash in the macOS menu bar, from /api/v1/summary, with only
// what every Mac has (curl, plutil).
func menuBarPlugin(serverURL, token string) string {
	if serverURL == "" {
		serverURL = "https://goliash.example.com"
	}
	if token == "" {
		token = "glsh_api_…" //nolint:gosec // a placeholder for the reader to replace
	}
	return strings.NewReplacer("$SERVER_URL", shellQuote(strings.TrimRight(serverURL, "/")), "$API_TOKEN", shellQuote(token)).Replace(menuBarSource)
}

const menuBarSource = `#!/bin/sh
# Goliash in the macOS menu bar, for SwiftBar or xbar: save it in the plugin folder as goliash.15m.sh (the 15m
# is how often it refreshes) and run chmod +x on it.
# <xbar.title>Goliash</xbar.title>
# <xbar.desc>Drift, updates, exploited vulnerabilities and how long production runs behind.</xbar.desc>
# <swiftbar.hideRunInTerminal>true</swiftbar.hideRunInTerminal>
# <swiftbar.hideDisablePlugin>true</swiftbar.hideDisablePlugin>
GOLIASH=$SERVER_URL
GOLIASH_TOKEN=$API_TOKEN # a viewer token: it can only read

if ! json=$(curl -fsS --max-time 20 -H "Authorization: Bearer $GOLIASH_TOKEN" "$GOLIASH/api/v1/summary" 2>&1); then
  echo "Goliash ?"
  echo "---"
  echo "Goliash cannot be read: $json | color=#e5484d"
  echo "Open Goliash | href=$GOLIASH"
  exit 0
fi
get() { printf '%s' "$json" | plutil -extract "$1" raw -o - - 2>/dev/null; }
drift=$(get drifting); updates=$(get updates); exploited=$(get exploited); eol=$(get end_of_life_passed)
median=$(get median_exposure_days); old=$(get behind_90_days); stale=$(get stale_targets)
: "${drift:=0}" "${updates:=0}" "${exploited:=0}" "${eol:=0}" "${median:=0}" "${old:=0}" "${stale:=0}"

if [ "$exploited" -gt 0 ] || [ "$eol" -gt 0 ]; then
  echo "Goliash $((exploited + eol)) | color=#e5484d"
elif [ "$drift" -gt 0 ]; then
  echo "Goliash $drift | color=#d9822b"
else
  echo "Goliash ✓"
fi
echo "---"
echo "$(get workspace) · $(get production)"
echo "$drift services drifting | href=$GOLIASH/"
echo "$updates updates waiting | href=$GOLIASH/updates"
echo "$exploited with a vulnerability exploited in the wild | href=$GOLIASH/security"
echo "$median days median behind, $old for 90+ days | href=$GOLIASH/security"
echo "$eol past end of life | href=$GOLIASH/security"
echo "$stale stale targets | href=$GOLIASH/status"
echo "---"
echo "Updated $(get generated)"
echo "Open Goliash | href=$GOLIASH"
echo "Refresh | refresh=true"
`
