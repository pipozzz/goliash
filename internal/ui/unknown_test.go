// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// A service whose newest release is unknown says why, and where to fix it: on its page and in the matrix.
func TestWhyUnknownShown(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	name := e.serviceOf("evil")
	svc, _ := e.st.GetServiceByName(ctx, sc, name)
	if _, err := e.st.InsertReleases(ctx, sc, svc.ID, []string{"latest", "edge"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RecordUpstreamCheck(ctx, sc, svc.ID, ""); err != nil {
		t.Fatal(err)
	}
	m := e.as(store.RoleMember)
	_, page := get(t, m, e.srv.URL+"/services/"+url.PathEscape(name), nil)
	for _, want := range []string{
		"Newest release unknown.", "None of the 2 tags of docker.io/library/nginx is comparable with &#34;1.27.2&#34;.",
		"Set a tag filter in the version policy", `href="/services/` + url.PathEscape(name) + `#policy"`, `id="policy"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("service page misses %q", want)
		}
	}
	if _, matrix := get(t, m, e.srv.URL+"/?view=table", nil); !strings.Contains(matrix, "no comparable tag") {
		t.Error("the matrix does not say why")
	}
}
