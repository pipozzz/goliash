// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

func TestCron(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04 Mon", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		spec string
		at   string
		want bool
	}{
		{"0 22 * * 5", "2026-10-23 22:00 Fri", true},
		{"0 22 * * 5", "2026-10-23 22:01 Fri", false},
		{"0 22 * * fri", "2026-10-23 22:00 Fri", true},
		{"*/15 9-17 * * mon-fri", "2026-10-20 09:45 Tue", true},
		{"*/15 9-17 * * mon-fri", "2026-10-25 09:45 Sun", false},
		{"0 0 * * 7", "2026-10-25 00:00 Sun", true},
		{"0 0 1 * 1", "2026-10-19 00:00 Mon", true}, // either day field, when both are restricted
		{"0 0 1 * 1", "2026-10-20 00:00 Tue", false},
		{"0 0 1 dec *", "2026-12-01 00:00 Tue", true},
		{"@daily", "2026-10-20 00:00 Tue", true},
		{"30 1,13 * * *", "2026-10-20 13:30 Tue", true},
	} {
		cs, err := parseCron(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		if got := cs.at(at(c.at)); got != c.want {
			t.Errorf("%s at %s = %v", c.spec, c.at, got)
		}
	}
	for _, bad := range []string{"", "* * * *", "61 * * * *", "* * * * mon-xyz", "5-1 * * * *", "*/0 * * * *"} {
		if _, err := parseCron(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func window(t *testing.T, m map[string]any) syncWindow {
	t.Helper()
	w, ok := parseSyncWindow(m)
	if !ok {
		t.Fatalf("window %v did not parse", m)
	}
	return w
}

// An open deny window blocks the applications it matches until it closes; allow windows block outside them.
func TestSyncBlocked(t *testing.T) {
	fri := time.Date(2026, 10, 23, 23, 30, 0, 0, time.UTC) // a Friday night
	weekend := window(t, map[string]any{"kind": "deny", "schedule": "0 22 * * 5", "duration": "56h", "applications": []any{"*-prod"}})
	why, until := syncBlocked([]syncWindow{weekend}, fri, "payments-prod", "payments")
	if why != `deny window "0 22 * * 5" for 56h` || !until.Equal(time.Date(2026, 10, 26, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekend freeze: %q until %v", why, until)
	}
	if why, _ := syncBlocked([]syncWindow{weekend}, fri, "payments-staging", "payments"); why != "" {
		t.Errorf("a window for other applications blocks: %q", why)
	}
	if why, _ := syncBlocked([]syncWindow{weekend}, fri.Add(-2*time.Hour), "payments-prod", "payments"); why != "" {
		t.Errorf("blocked before the window opens: %q", why)
	}

	office := window(t, map[string]any{"kind": "allow", "schedule": "0 9 * * mon-fri", "duration": "8h", "namespaces": []any{"pay*"}})
	why, until = syncBlocked([]syncWindow{office}, fri, "payments-prod", "payments")
	if why != `outside the allow window "0 9 * * mon-fri"` || !until.Equal(time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("outside office hours: %q until %v", why, until)
	}
	if why, _ := syncBlocked([]syncWindow{office}, time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC), "payments-prod", "payments"); why != "" {
		t.Errorf("blocked in office hours: %q", why)
	}

	prague := window(t, map[string]any{"kind": "deny", "schedule": "0 22 * * *", "duration": "1h", "timeZone": "Europe/Prague", "clusters": []any{"https://kubernetes.default.svc"}})
	if why, _ := syncBlocked([]syncWindow{prague}, time.Date(2026, 10, 20, 20, 30, 0, 0, time.UTC), "x", "y", "https://kubernetes.default.svc"); why == "" {
		t.Error("a time-zoned window did not block") // 22:30 in Prague
	}

	both := window(t, map[string]any{"kind": "deny", "schedule": "* * * * *", "duration": "1h", "applications": []any{"a"}, "namespaces": []any{"n"}, "andOperator": true})
	if !both.matches("a", "n") || both.matches("a", "other") {
		t.Error("andOperator")
	}
	for _, bad := range []map[string]any{
		{"kind": "deny", "schedule": "nope", "duration": "1h"},
		{"kind": "deny", "schedule": "* * * * *", "duration": "soon"},
		{"kind": "maybe", "schedule": "* * * * *", "duration": "1h"},
	} {
		if _, ok := parseSyncWindow(bad); ok {
			t.Errorf("%v parsed", bad)
		}
	}
}

// The collector reads the AppProjects' sync windows and reports the applications they block.
func TestArgoCDSyncWindows(t *testing.T) {
	app := argoApp("payments-prod", map[string]any{
		"project":     "shop",
		"source":      map[string]any{"repoURL": "https://github.com/acme/deploy.git", "path": "apps/payments/prod"},
		"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "payments"},
	}, map[string]any{
		"sync":      map[string]any{"status": "Synced"},
		"resources": []any{map[string]any{"group": "apps", "kind": "Deployment", "namespace": "payments", "name": "payments-api"}},
	})
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "AppProject",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd"},
		"spec": map[string]any{"syncWindows": []any{
			map[string]any{"kind": "deny", "schedule": "0 22 * * 5", "duration": "56h", "namespaces": []any{"payments"}},
		}},
	}}
	c := newCollector(fake.NewClientset(fixtures()...), agentproto.KubernetesSettings{})
	c.dyn = fakeDynamic(app, project)
	c.clock = func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }
	start(t, c, func() {})
	g := byName(collect(t, c).Workloads)["payments/payments-api"].Gitops
	if g == nil || g.SyncBlocked == nil || *g.SyncBlocked != `deny window "0 22 * * 5" for 56h` ||
		g.SyncBlockedUntil == nil || !g.SyncBlockedUntil.Equal(time.Date(2026, 10, 26, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("gitops %+v", g)
	}
}
