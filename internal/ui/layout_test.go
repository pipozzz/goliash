// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every table sits in a .scroll wrapper, so a wide one scrolls inside its panel on a
// phone instead of widening the whole page.
func TestTablesScrollOnPhones(t *testing.T) {
	files, _ := filepath.Glob("*.templ")
	table := regexp.MustCompile(`^\s*<table\b`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if table.MatchString(l) && (i == 0 || !strings.Contains(lines[i-1], `class="scroll`)) {
				t.Errorf("%s:%d: a table outside <div class=\"scroll\">", f, i+1)
			}
		}
	}
}

// The skip link is the first thing the keyboard reaches, and it lands on <main>, which
// takes focus (tabindex="-1") so the next Tab continues inside the page, not in the header.
func TestSkipLinkLandsOnMain(t *testing.T) {
	var b strings.Builder
	if err := Layout(Base{Title: "Matrix", Page: "matrix", Email: "you@example.com", Role: "owner"}).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	link := strings.Index(out, `<a class="skip-link" href="#content">`)
	if link < 0 {
		t.Fatal("no skip link")
	}
	if header := strings.Index(out, `<header`); header >= 0 && link > header {
		t.Error("the skip link comes after the header, so it is not the first stop")
	}
	if !strings.Contains(out, `<main id="content" tabindex="-1">`) {
		t.Error(`the skip link's target is not <main id="content" tabindex="-1">`)
	}
}

// A kiosk page has no header to skip, so it has no skip link either.
func TestKioskHasNoSkipLink(t *testing.T) {
	var b strings.Builder
	if err := Layout(Base{Title: "Tiles", Page: "tiles", Kiosk: true}).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "skip-link") {
		t.Error("a kiosk page has a skip link")
	}
}

// role="menu" and role="menuitem" promise arrow-key navigation that our <details>
// disclosures of plain links do not have, which leaves a screen reader worse off than
// the markup alone. Keep them out.
func TestNoFalseMenuRoles(t *testing.T) {
	files, _ := filepath.Glob("*.templ")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, l := range strings.Split(string(b), "\n") {
			if strings.Contains(l, "<!--") {
				continue // the comment that explains why
			}
			if strings.Contains(l, `role="menu"`) || strings.Contains(l, `role="menuitem"`) {
				t.Errorf("%s:%d: an ARIA menu role on markup that is not a menu", f, i+1)
			}
		}
	}
}

// Header cells name their column, if only for screen readers (an actions column says
// so in a .sr-only span).
func TestNoEmptyHeaderCells(t *testing.T) {
	files, _ := filepath.Glob("*.templ")
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "<th></th>") {
			t.Errorf("%s: an empty <th>", f)
		}
	}
}
