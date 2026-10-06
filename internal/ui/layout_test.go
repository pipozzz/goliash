// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
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
