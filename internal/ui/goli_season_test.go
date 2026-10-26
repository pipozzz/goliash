// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
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

// Ten unread notifications or more put Goli in its swamped outfit, with its sigh; fewer do not.
func TestGoliSwamped(t *testing.T) {
	render := func(unread int) string {
		var b strings.Builder
		ctx := templ.WithChildren(context.Background(), templ.NopComponent)
		if err := Layout(Base{Goli: true, Unread: unread, Email: "a@example.com"}).Render(ctx, &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if page := render(12); !strings.Contains(page, `<html lang="en" class="goli-swamped" data-swamped-at="10">`) || !strings.Contains(page, "Always something!") || !strings.Contains(page, `class="goli-swamped-outfit"`) {
		t.Errorf("12 unread: %.300s", page)
	}
	if page := render(3); strings.Contains(page, `class="goli-swamped"`) {
		t.Error("3 unread swamp Goli")
	}
	defer func() { goliSeasonSetting = "" }()
	goliSeasonSetting = "swamped"
	if !goliSwampedFor(0) || swampedThreshold() != 0 {
		t.Error("the setting does not swamp Goli")
	}
}
