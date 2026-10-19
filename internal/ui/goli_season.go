// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import "time"

// goliSeasonSetting is GOLIASH_GOLI_SEASON: "" follows the calendar, "off" never dresses Goli up, a season's name
// always wears it.
var goliSeasonSetting string

// goliSeason is what Goli wears on a day: a pumpkin around Halloween, a Santa hat in December, a scarf in January
// and February, sunglasses in July and August, nothing else the rest of the year.
func goliSeason(now time.Time) string {
	switch goliSeasonSetting {
	case "":
	case "off":
		return ""
	default:
		return goliSeasonSetting
	}
	m, d := now.Month(), now.Day()
	switch {
	case m == time.October && d >= 15, m == time.November && d <= 2:
		return "halloween"
	case m == time.December:
		return "christmas"
	case m == time.January, m == time.February:
		return "winter"
	case m == time.July, m == time.August:
		return "summer"
	}
	return ""
}

// seasonClass marks the mascot with its season, for CSS ("goli-halloween"); "" out of season.
func seasonClass(season string) string {
	if season == "" {
		return ""
	}
	return "goli-" + season
}
