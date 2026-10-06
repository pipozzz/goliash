// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"sort"
	"strings"
)

// AppName is an application as stored with a workload, and where the name came from.
type AppName struct{ App, Source string }

// AppFamily is what an application is shown under: its project, when platforms
// like Dokploy or Nomploy deployed its parts as separate apps named
// "<project>-<part>" (cefiro, cefiro-db, cefiro-redis -> cefiro), else itself.
type AppFamily struct {
	Name    string
	Members []string // the applications merged into it, when more than one
}

// derivedSources are the app sources that come from generated names, not from a
// label someone chose: only those are merged. An explicit label stays as it is.
var derivedSources = map[string]bool{
	"nomad job": true, "namespace": true, "com.docker.compose.project": true, "com.docker.stack.namespace": true,
}

// AppFamilies maps each application to its family. Applications from generated
// names whose first word is shared with another application ("goliash-db" and the
// "goliash" job) are merged under that word.
func AppFamilies(apps []AppName) map[string]AppFamily {
	byFirst := map[string]map[string]bool{}
	for _, a := range apps {
		if a.App == "" || !derivedSources[a.Source] {
			continue
		}
		first, _, _ := strings.Cut(a.App, "-")
		if byFirst[first] == nil {
			byFirst[first] = map[string]bool{}
		}
		byFirst[first][a.App] = true
	}
	out := map[string]AppFamily{}
	for _, a := range apps {
		if _, done := out[a.App]; done || a.App == "" {
			continue
		}
		out[a.App] = AppFamily{Name: a.App}
		if !derivedSources[a.Source] {
			continue
		}
		first, _, _ := strings.Cut(a.App, "-")
		if members := byFirst[first]; len(members) > 1 {
			names := make([]string, 0, len(members))
			for m := range members {
				names = append(names, m)
			}
			sort.Strings(names)
			out[a.App] = AppFamily{Name: first, Members: names}
		}
	}
	return out
}
