// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestLayerChange(t *testing.T) {
	base := store.ImageLayer{Digest: "a", Size: 30, Command: "ADD rootfs /"}
	before := []store.ImageLayer{base, {Digest: "b", Size: 10, Command: "RUN npm ci"}, {Digest: "c", Size: 1, Command: "COPY . /app"}}
	after := []store.ImageLayer{base, {Digest: "d", Size: 120, Command: "RUN apt-get install -y chromium"}, {Digest: "e", Size: 12, Command: "RUN npm ci"}, {Digest: "f", Size: 1, Command: "COPY . /app"}}
	added, removed := LayerChange(before, after)
	if len(added) != 3 || added[0].Command != "RUN apt-get install -y chromium" || len(removed) != 2 || removed[0].Digest != "b" {
		t.Fatalf("added %+v removed %+v", added, removed)
	}
}
