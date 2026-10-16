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
