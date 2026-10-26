// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestVMReportPage(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	svc, _ := e.st.GetServiceByName(ctx, sc, e.serviceOf("evil"))
	if err := e.st.SetRemediation(ctx, sc, store.Remediation{Exploited: 7, Security: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateAck(ctx, store.Ack{Scope: sc, ServiceID: svc.ID, Kind: "release", UntilVersion: "9.0.0", CreatedBy: "ciso@example.com"}); err != nil {
		t.Fatal(err)
	}
	viewer := e.as(store.RoleViewer)
	month := time.Now().UTC().Format("2006-01")
	code, page := get(t, viewer, e.srv.URL+"/security/report?month="+month, nil)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{
		"Vulnerability management: ", "<strong>7 days</strong><span>exploited in the wild</span>", "<strong>not set</strong><span>after end of life</span>",
		"Accepted risks", "ciso@example.com", "version 9.0.0", "CISA", `href="/security/report.csv?month=` + month + `"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if strings.Contains(page, ">"+time.Now().UTC().AddDate(0, 1, 0).Format("January")+" →<") {
		t.Error("the report links a month that has not started")
	}

	code, body := get(t, viewer, e.srv.URL+"/security/report.csv?month="+month, nil)
	if code != http.StatusOK || !strings.HasPrefix(body, "service,owner,from,to,behind_since,deployed,days_to_patch,security_fix,within_deadline") {
		t.Errorf("csv %d %q", code, body)
	}
	if code, _ := get(t, viewer, e.srv.URL+"/security/report.csv?month=bad", nil); code != http.StatusBadRequest {
		t.Errorf("bad month: %d", code)
	}
	if _, sec := get(t, viewer, e.srv.URL+"/security", nil); !strings.Contains(sec, `href="/security/report"`) {
		t.Error("the Security page does not link the report")
	}
}
