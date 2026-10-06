// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package nomad

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

func fakeNomad(t *testing.T, token string) *httptest.Server {
	responses := map[string]string{
		"/v1/jobs": `[
			{"ID":"api","Namespace":"prod","Type":"service","Status":"running"},
			{"ID":"logs","Namespace":"prod","Type":"system","Status":"running"},
			{"ID":"old","Namespace":"prod","Type":"service","Status":"dead"},
			{"ID":"script","Namespace":"default","Type":"batch","Status":"running"},
			{"ID":"secret","Namespace":"default","Type":"service","Status":"running"}
		]`,
		// Mid-deployment: version 3 (1.5.0) replacing version 2 (1.4.2).
		"/v1/job/api/versions": `{"Versions":[
			{"ID":"api","Namespace":"prod","Version":3,"Meta":{"goliash.service":"payments-api"},"TaskGroups":[
				{"Name":"web","Count":3,"Tasks":[
					{"Name":"app","Driver":"docker","Config":{"image":"ghcr.io/acme/payments-api:1.5.0"}},
					{"Name":"proxy","Driver":"docker","Config":{"image":"envoyproxy/envoy:v1.31.2"}}]}]},
			{"ID":"api","Namespace":"prod","Version":2,"TaskGroups":[
				{"Name":"web","Count":3,"Tasks":[
					{"Name":"app","Driver":"docker","Config":{"image":"ghcr.io/acme/payments-api:1.4.2"}},
					{"Name":"proxy","Driver":"docker","Config":{"image":"envoyproxy/envoy:v1.31.2"}}]}]}]}`,
		"/v1/job/api/allocations": `[
			{"TaskGroup":"web","JobVersion":2,"ClientStatus":"running"},
			{"TaskGroup":"web","JobVersion":2,"ClientStatus":"running"},
			{"TaskGroup":"web","JobVersion":3,"ClientStatus":"running"},
			{"TaskGroup":"web","JobVersion":3,"ClientStatus":"pending"},
			{"TaskGroup":"web","JobVersion":1,"ClientStatus":"complete"}]`,
		"/v1/job/logs/versions": `{"Versions":[{"ID":"logs","Namespace":"prod","Version":0,"TaskGroups":[
			{"Name":"collector","Count":1,"Tasks":[{"Name":"vector","Driver":"docker","Config":{"image":"timberio/vector:0.41.1-alpine"}}]}]}]}`,
		"/v1/job/logs/allocations": `[{"TaskGroup":"collector","JobVersion":0,"ClientStatus":"running"},
			{"TaskGroup":"collector","JobVersion":0,"ClientStatus":"running"}]`,
		"/v1/job/script/versions": `{"Versions":[{"ID":"script","Namespace":"default","Version":0,"TaskGroups":[
			{"Name":"run","Count":1,"Tasks":[{"Name":"sh","Driver":"raw_exec","Config":{"command":"/bin/true"}}]}]}]}`,
		"/v1/job/script/allocations": `[]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("collector sent %s", r.Method)
		}
		if r.Header.Get("X-Nomad-Token") != token {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("Permission denied"))
			return
		}
		if r.URL.Path == "/v1/jobs" && r.URL.Query().Get("namespace") != "*" {
			t.Errorf("jobs listed without namespace=*: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("region") != "eu" {
			t.Errorf("region not passed: %s", r.URL.RawQuery)
		}
		body, ok := responses[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("Permission denied"))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCollect(t *testing.T) {
	srv := fakeNomad(t, "read-token")
	t.Setenv("GOLIASH_CREDENTIAL_NOMAD_PROD", "read-token")
	ref, region := "nomad-prod", "eu"
	c, err := New(context.Background(), agentproto.Target{
		CredentialsRef: &ref, Nomad: &agentproto.NomadSettings{Address: srv.URL + "/", Region: &region},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// The "secret" job is forbidden: reported, others still collected.
	if res.Complete || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "default/secret") {
		t.Fatalf("complete=%v errors=%v", res.Complete, res.Errors)
	}
	if len(res.Workloads) != 2 {
		t.Fatalf("workloads %+v", res.Workloads)
	}

	api := res.Workloads[0]
	if api.ID != "prod/api" || api.Kind != agentproto.NomadJob || *api.Namespace != "prod" || *api.DesiredReplicas != 3 ||
		api.Labels["goliash.service"] != "payments-api" {
		t.Fatalf("api %+v", api)
	}
	var got []string
	for _, c := range api.Containers {
		got = append(got, c.Name+"="+c.Image+"x"+strconv.Itoa(c.Running))
	}
	want := "web/app=ghcr.io/acme/payments-api:1.4.2x2 web/app=ghcr.io/acme/payments-api:1.5.0x1 web/proxy=envoyproxy/envoy:v1.31.2x3"
	if strings.Join(got, " ") != want {
		t.Fatalf("containers\n got: %s\nwant: %s", strings.Join(got, " "), want)
	}

	logs := res.Workloads[1]
	if logs.Name != "logs" || logs.DesiredReplicas != nil || logs.Containers[0].Running != 2 {
		t.Fatalf("system job %+v", logs)
	}
}

func TestMissingCredential(t *testing.T) {
	t.Setenv("GOLIASH_CREDENTIALS_DIR", t.TempDir())
	ref := "absent"
	if _, err := New(context.Background(), agentproto.Target{CredentialsRef: &ref, Nomad: &agentproto.NomadSettings{Address: "http://n"}}); err == nil {
		t.Fatal("missing credential accepted")
	}
}

func TestListForbidden(t *testing.T) {
	srv := fakeNomad(t, "right")
	region := "eu"
	t.Setenv("NOMAD_TOKEN", "wrong")
	c, _ := New(context.Background(), agentproto.Target{Nomad: &agentproto.NomadSettings{Address: srv.URL, Region: &region}})
	if _, err := c.Collect(context.Background()); err == nil || !strings.Contains(err.Error(), "403") ||
		!strings.Contains(err.Error(), "needs list-jobs and read-job") {
		t.Fatalf("err = %v", err)
	}
}

func TestListForbiddenWithoutToken(t *testing.T) {
	srv := fakeNomad(t, "right")
	t.Setenv("NOMAD_TOKEN", "")
	c, _ := New(context.Background(), agentproto.Target{Nomad: &agentproto.NomadSettings{Address: srv.URL}})
	if _, err := c.Collect(context.Background()); err == nil || !strings.Contains(err.Error(), "no ACL token") {
		t.Fatalf("err = %v", err)
	}
}

// Runs of a periodic job count as the job's: one workload, not one per run.
func TestPeriodicRuns(t *testing.T) {
	job := `{"Versions":[{"ID":"%s","Namespace":"prod","Version":0,"TaskGroups":[
		{"Name":"b","Count":1,"Tasks":[{"Name":"dump","Driver":"docker","Config":{"image":"postgres:17"}}]}]}]}`
	responses := map[string]string{
		"/v1/jobs": `[
			{"ID":"backup","Namespace":"prod","Type":"batch","Status":"running","Periodic":true},
			{"ID":"backup/periodic-1759740000","Namespace":"prod","Type":"batch","Status":"running","ParentID":"backup"},
			{"ID":"backup/periodic-1759653600","Namespace":"prod","Type":"batch","Status":"dead","ParentID":"backup"},
			{"ID":"export/dispatch-1759740000-ab12cd34","Namespace":"prod","Type":"batch","Status":"running","ParentID":"export"}
		]`,
		"/v1/job/backup/versions":                                 strings.ReplaceAll(job, "%s", "backup"),
		"/v1/job/backup/allocations":                              `[]`,
		"/v1/job/backup/periodic-1759740000/versions":             strings.ReplaceAll(job, "%s", "backup/periodic-1759740000"),
		"/v1/job/backup/periodic-1759740000/allocations":          `[{"TaskGroup":"b","JobVersion":0,"ClientStatus":"running"}]`,
		"/v1/job/export/dispatch-1759740000-ab12cd34/versions":    strings.ReplaceAll(job, "%s", "export/dispatch-1759740000-ab12cd34"),
		"/v1/job/export/dispatch-1759740000-ab12cd34/allocations": `[{"TaskGroup":"b","JobVersion":0,"ClientStatus":"running"}]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := responses[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(context.Background(), agentproto.Target{Nomad: &agentproto.NomadSettings{Address: srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, w := range res.Workloads {
		got = append(got, w.ID+":"+string(w.Kind)+":"+strconv.Itoa(w.Containers[0].Running))
	}
	if strings.Join(got, " ") != "prod/backup:cronjob:1 prod/export:cronjob:1" || !res.Complete {
		t.Fatalf("workloads %v (complete %v, errors %v)", got, res.Complete, res.Errors)
	}
}
