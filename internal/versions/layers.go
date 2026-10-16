// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"sort"

	"github.com/pipozzz/goliash/internal/store"
)

// LayerChange is what changed between two images' layers: the layers only the new one has, and those only the old
// one had, biggest first. Shared layers (the base, unchanged steps) are left out: they are not what grew.
func LayerChange(before, after []store.ImageLayer) (added, removed []store.ImageLayer) {
	had, has := map[string]bool{}, map[string]bool{}
	for _, l := range before {
		had[l.Digest] = true
	}
	for _, l := range after {
		has[l.Digest] = true
		if !had[l.Digest] {
			added = append(added, l)
		}
	}
	for _, l := range before {
		if !has[l.Digest] {
			removed = append(removed, l)
		}
	}
	bySize := func(ls []store.ImageLayer) {
		sort.SliceStable(ls, func(i, j int) bool { return ls[i].Size > ls[j].Size })
	}
	bySize(added)
	bySize(removed)
	return added, removed
}

// SizeChange is what the last deploy in an environment changed in the image: its size and its layers.
type SizeChange struct {
	Env, From, To    string // environment, and the versions before and after
	Before, After    int64  // sizes in bytes
	Added, Removed   []store.ImageLayer
	AddedN, RemovedN int // all of them, when the lists are cut short
}

// SizeChangeLayers is how many added and removed layers a size change lists.
const SizeChangeLayers = 6

// LastSizeChange is the most recent deploy, production first, whose two images' layers are known.
func LastSizeChange(periods []store.ImagePeriod, evidence map[string]store.ImageEvidence, envs []store.Environment) *SizeChange {
	ev := func(p store.ImagePeriod) store.ImageEvidence {
		return evidence[ParseImage(p.Image).Repo()+"@"+p.Digest]
	}
	for i := len(envs) - 1; i >= 0; i-- {
		var cur, prev *store.ImagePeriod
		for j := range periods {
			p := &periods[j]
			if p.EnvironmentID == envs[i].ID && p.Until.IsZero() && (cur == nil || p.Since.After(cur.Since)) {
				cur = p
			}
		}
		if cur == nil {
			continue
		}
		for j := range periods {
			p := &periods[j]
			if p.EnvironmentID == envs[i].ID && p.Digest != cur.Digest && !p.Until.IsZero() && (prev == nil || p.Until.After(prev.Until)) {
				prev = p
			}
		}
		if prev == nil || len(ev(*cur).Layers) == 0 || len(ev(*prev).Layers) == 0 {
			continue
		}
		added, removed := LayerChange(ev(*prev).Layers, ev(*cur).Layers)
		ch := &SizeChange{
			Env: envs[i].Name, From: prev.Tag, To: cur.Tag, Before: ev(*prev).Size, After: ev(*cur).Size,
			AddedN: len(added), RemovedN: len(removed),
		}
		ch.Added, ch.Removed = added[:min(len(added), SizeChangeLayers)], removed[:min(len(removed), SizeChangeLayers)]
		return ch
	}
	return nil
}
