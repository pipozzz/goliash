// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package goli

import (
	"fmt"
	"time"
)

// Usage is what Goli used today (UTC) on this server: questions and tokens, so whoever pays sees it without the
// provider's console. It starts over at midnight UTC and on restart.
type Usage struct {
	Day       string // YYYY-MM-DD, UTC
	Questions int
	PerDay    int   // the daily ceiling; 0 for none
	Input     int64 // input tokens, cached ones included
	Cached    int64 // input tokens read from the provider's cache, billed at a fraction
	Output    int64
}

// Summary says it in a line.
func (u Usage) Summary() string {
	q := fmt.Sprintf("%d questions", u.Questions)
	if u.Questions == 1 {
		q = "1 question"
	}
	if u.PerDay > 0 {
		q += fmt.Sprintf(" of %d allowed", u.PerDay)
	}
	return fmt.Sprintf("Today: %s, %s input tokens (%s from cache), %s output tokens.", q, short(u.Input), short(u.Cached), short(u.Output))
}

func short(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// rollover starts a new day's count; a.mu is held.
func (a *Assistant) rollover(now time.Time) {
	if day := now.UTC().Format(time.DateOnly); day != a.day {
		a.day, a.today, a.tokIn, a.tokCached, a.tokOut = day, 0, 0, 0, 0
	}
}

// record adds the tokens of one model call.
func (a *Assistant) record(input, cached, output int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rollover(time.Now())
	a.tokIn += input
	a.tokCached += cached
	a.tokOut += output
}

// Usage is what Goli used today.
func (a *Assistant) Usage() Usage {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rollover(time.Now())
	return Usage{Day: a.day, Questions: a.today, PerDay: a.cfg.PerDay, Input: a.tokIn, Cached: a.tokCached, Output: a.tokOut}
}
