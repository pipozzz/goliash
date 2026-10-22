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

// An admin sets remediation deadlines on the Security page; Updates, its CSV and the Security page then say what is
// due. A member may not set them.
func TestRemediationDeadlines(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	admin, member, viewer := e.as(store.RoleAdmin), e.as(store.RoleMember), e.as(store.RoleViewer)
	svc, _ := e.st.GetServiceByName(ctx, e.ws.Scope(), e.serviceOf("evil"))
	if _, err := e.st.InsertReleases(ctx, e.ws.Scope(), svc.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReleaseSecurity(ctx, e.ws.Scope(), svc.ID, "1.28.0", "CVE-2026-1"); err != nil {
		t.Fatal(err)
	}
	_ = e.ui.checker.EvaluateDrift(ctx, e.ws.Scope())
	if _, page := get(t, viewer, e.srv.URL+"/updates", nil); strings.Contains(page, "due in") {
		t.Fatal("a deadline before any is set")
	}
	form := url.Values{"exploited": {"7"}, "security": {"3"}, "eol": {"30"}}
	if code, _, _ := post(t, member, e.srv.URL+"/security/deadlines", form); code != 403 && code != 404 {
		t.Fatalf("a member set deadlines: %d", code)
	}
	if code, _, _ := post(t, admin, e.srv.URL+"/security/deadlines", url.Values{"exploited": {"5"}, "security": {"3"}, "eol": {"30"}}); code >= 400 {
		t.Fatalf("odd days accepted? %d", code)
	}
	if rem, _ := e.st.Remediation(ctx, e.ws.Scope()); rem.Any() {
		t.Fatalf("days off the list were saved: %+v", rem)
	}
	post(t, admin, e.srv.URL+"/security/deadlines", form)
	if rem, _ := e.st.Remediation(ctx, e.ws.Scope()); rem != (store.Remediation{Exploited: 7, Security: 3, EOL: 30}) {
		t.Fatalf("deadlines %+v", rem)
	}
	_, page := get(t, viewer, e.srv.URL+"/updates", nil)
	if !strings.Contains(page, `class="badge warn" href="/security#deadlines"`) || !strings.Contains(page, ">due in 3 days</a>") {
		t.Error("Updates does not mark the deadline")
	}
	_, body := get(t, viewer, e.srv.URL+"/updates.csv", nil)
	if !strings.Contains(body, ",due,days_left,") || !strings.Contains(body, ",3,") {
		t.Errorf("CSV misses the deadline: %q", body)
	}
	if _, body = get(t, viewer, e.srv.URL+"/updates?due=over", nil); strings.Contains(body, "1.28.0</") {
		t.Error("due=over shows an upgrade that is not past its deadline")
	}
	_, page = get(t, viewer, e.srv.URL+"/security", nil)
	for _, want := range []string{`id="deadlines"`, "security fix: 3 days", "nothing past deadline", "1 due within a week"} {
		if !strings.Contains(page, want) {
			t.Errorf("Security page misses %q", want)
		}
	}
	if strings.Contains(page, `action="/security/deadlines"`) {
		t.Error("a viewer is offered the deadlines form")
	}
	post(t, admin, e.srv.URL+"/security/deadlines", url.Values{"exploited": {"0"}, "security": {"0"}, "eol": {"0"}})
	if rem, _ := e.st.Remediation(ctx, e.ws.Scope()); rem.Any() {
		t.Fatalf("deadlines not cleared: %+v", rem)
	}
}
