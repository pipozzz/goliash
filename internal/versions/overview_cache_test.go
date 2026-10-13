// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"testing"
)

// The overview reuses releases between page loads, but a new release written in this process shows at once.
func TestReleaseCacheFollowsWrites(t *testing.T) {
	ctx := context.Background()
	l := newLab(t)
	l.run(map[string]string{"prod-a": "1.27.2"})
	if err := l.checker.CheckUpstreams(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	o, err := LoadOverview(ctx, l.st, l.sc)
	if err != nil || o.Upstreams[l.svc.ID].Latest.Raw != "1.28.0" {
		t.Fatalf("before %+v %v", o.Upstreams[l.svc.ID], err)
	}
	if _, err := l.st.InsertReleases(ctx, l.sc, l.svc.ID, []string{"1.29.0"}); err != nil {
		t.Fatal(err)
	}
	if o, _ = LoadOverview(ctx, l.st, l.sc); o.Upstreams[l.svc.ID].Latest.Raw != "1.29.0" {
		t.Fatalf("cache kept an old release: %+v", o.Upstreams[l.svc.ID])
	}
}
