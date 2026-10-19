// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"testing"
	"time"
)

func TestGoliSeason(t *testing.T) {
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 12, 0, 0, 0, time.UTC) }
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{day(time.October, 14), ""},
		{day(time.October, 15), "halloween"},
		{day(time.October, 31), "halloween"},
		{day(time.November, 2), "halloween"},
		{day(time.November, 3), ""},
		{day(time.December, 24), "christmas"},
		{day(time.January, 10), "winter"},
		{day(time.February, 28), "winter"},
		{day(time.March, 1), ""},
		{day(time.July, 1), "summer"},
		{day(time.September, 1), ""},
	} {
		if got := goliSeason(c.at); got != c.want {
			t.Errorf("%s: %q, want %q", c.at.Format("Jan 2"), got, c.want)
		}
	}
	defer func() { goliSeasonSetting = "" }()
	goliSeasonSetting = "off"
	if goliSeason(day(time.October, 31)) != "" {
		t.Error("off still dresses Goli up")
	}
	goliSeasonSetting = "summer"
	if goliSeason(day(time.October, 31)) != "summer" {
		t.Error("the setting is not worn")
	}
}
