// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

func TestKinds(t *testing.T) {
	o := versions.Overview{Targets: []store.Target{{ID: "t1", Platform: "lambda"}, {ID: "t2", Platform: "kubernetes"}, {ID: "t3", Platform: "rds"}}}
	if got := rowKinds(o, []string{"t1", "t2", "t1"}); strings.Join(got, ",") != "kubernetes,lambda" {
		t.Fatalf("kinds %v", got)
	}
	rows := []MatrixRow{{Service: "fn", Kinds: []string{"lambda"}}, {Service: "db", Kinds: []string{"rds"}}}
	if !strings.Contains(rowSearch(rows[0]), "type:lambda") {
		t.Error("the filter cannot find a row by its kind")
	}
	if opts := kindOptions(rows); len(opts) != 2 || opts[0].Key != "lambda" || opts[1].Label != "RDS" {
		t.Errorf("options %+v", opts)
	}
	if kindOptions(rows[:1]) != nil {
		t.Error("one kind is no filter")
	}
}

func TestRowWhat(t *testing.T) {
	for repo, want := range map[string]string{
		"rds.amazonaws.com/aurora-postgresql": "Aurora PostgreSQL · RDS",
		"elasticache.amazonaws.com/valkey":    "Valkey · ElastiCache",
		"opensearch.amazonaws.com/opensearch": "OpenSearch Service",
		"public.ecr.aws/lambda/nodejs":        "Node.js runtime",
		"ghcr.io/acme/api":                    "",
		"kafka.amazonaws.com/kafka":           "Kafka · MSK",
	} {
		if got := rowWhat(repo); got != want {
			t.Errorf("%s: %q, want %q", repo, got, want)
		}
	}
	if kindGroup(MatrixRow{Kinds: []string{"lambda"}}) != "Functions" || kindGroupIcon("Databases") != "rds" {
		t.Error("kind groups")
	}
}

// The copies of one service (same image, same versions) fold in the matrix; a copy behind stays apart.
func TestFoldKeys(t *testing.T) {
	cell := func(tag string) MatrixCell { return MatrixCell{Versions: []VersionView{{Tag: tag}}} }
	rows := foldKeys("api", []MatrixRow{
		{Service: "api-pr-1", Repo: "ghcr.io/acme/api", Health: "ok", Cells: []MatrixCell{cell("1.2")}},
		{Service: "api-pr-2", Repo: "ghcr.io/acme/api", Health: "ok", Cells: []MatrixCell{cell("1.2")}},
		{Service: "api-pr-3", Repo: "ghcr.io/acme/api", Health: "warn", Cells: []MatrixCell{cell("1.1")}},
		{Service: "web", Health: "ok", Cells: []MatrixCell{cell("1.2")}},
	})
	if rows[0].Same == "" || rows[0].Same != rows[1].Same || rows[2].Same != "" || rows[3].Same != "" {
		t.Fatalf("fold keys %q %q %q %q", rows[0].Same, rows[1].Same, rows[2].Same, rows[3].Same)
	}
}

func TestAWSUpgradeCommand(t *testing.T) {
	for _, c := range []struct{ repo, cur, to, want string }{
		{"rds.amazonaws.com/postgres", "15.6", "16.4", "aws rds modify-db-instance --db-instance-identifier db --engine-version 16.4 --allow-major-version-upgrade --apply-immediately --region eu-west-1"},
		{"rds.amazonaws.com/aurora-mysql", "3.05", "3.07", "aws rds modify-db-cluster --db-cluster-identifier db --engine-version 3.07 --apply-immediately --region eu-west-1"},
		{"elasticache.amazonaws.com/redis", "6.2", "7.1", "aws elasticache modify-replication-group --replication-group-id db --engine-version 7.1 --apply-immediately --region eu-west-1"},
		{"eks.amazonaws.com/kubernetes", "1.31", "1.32", "aws eks update-cluster-version --name db --kubernetes-version 1.32 --region eu-west-1"},
		{"opensearch.amazonaws.com/opensearch", "2.11", "2.19", "aws opensearch upgrade-domain --domain-name db --target-version OpenSearch_2.19 --region eu-west-1"},
		{"public.ecr.aws/lambda/nodejs", "22", "24", "aws lambda update-function-configuration --function-name db --runtime nodejs24.x --region eu-west-1"},
		{"ghcr.io/acme/api", "1", "2", ""},
	} {
		if got := awsUpgradeCommand(c.repo, "db", "eu-west-1", c.cur, c.to); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.repo, got, c.want)
		}
	}
	if targetRegion([]byte(`{"rds":{"region":"eu-central-1"}}`)) != "eu-central-1" || targetRegion([]byte(`{"docker":{}}`)) != "" {
		t.Error("region")
	}
}

func TestCalendarMonths(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	items := []UpdateItem{
		{Service: "db", EOL: "2026-11-13"},
		{Service: "api", Due: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), DueFor: "exploited vulnerability"},
		{Service: "web"},
		{Service: "cache", EOL: "2026-12-01", Due: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), DueFor: "critical"},
	}
	ms := calendarMonths(items, now)
	if len(ms) != 3 || ms[0].Label != "Past" || ms[0].Items[0].Service != "api" || ms[1].Label != "November 2026" ||
		len(ms[1].Items) != 2 || ms[1].Items[0].Service != "cache" || ms[1].Items[1].What != "end of life" || ms[2].Label != "December 2026" {
		t.Fatalf("months %+v", ms)
	}
}

// With AWS, the agent page lists each service: watched, refused with the actions to allow, or not set up; and who it
// is in AWS.
func TestAWSSourceRows(t *testing.T) {
	notes := []string{
		"msk eu-central-1: operation error Kafka: ListClustersV2, https response error StatusCode: 403, AccessDeniedException",
		"aws ec2: instance i-0abc in eu-central-1, role dev-gw",
	}
	rows := agentSources("docker", []string{"docker", "rds"}, nil, notes)
	state := map[string]SourceRow{}
	for _, r := range rows {
		state[r.Label] = r
	}
	if r := state["AWS identity"]; r.State != "watching" || !strings.Contains(r.Detail, "role dev-gw") {
		t.Errorf("identity %+v", r)
	}
	if state["RDS and Aurora databases"].State != "watching" || state["Lambda functions"].State != "off" {
		t.Errorf("rds %+v lambda %+v", state["RDS and Aurora databases"], state["Lambda functions"])
	}
	if r := state["MSK (Kafka) clusters"]; r.State != "denied" || !strings.Contains(r.Detail, "kafka:ListClustersV2") {
		t.Errorf("msk %+v", r)
	}
	// No AWS at all: one row to set it up.
	if rows := agentSources("docker", []string{"docker"}, nil, nil); rows[len(rows)-2].Key != "aws" || rows[len(rows)-2].State != "off" {
		t.Errorf("without AWS %+v", rows)
	}
}

// Members tick services on the matrix and give them one team.
func TestMatrixBulkTeam(t *testing.T) {
	e := newUIEnv(t)
	member := e.as(store.RoleMember)
	_, page := get(t, member, e.srv.URL+"/", nil)
	if !strings.Contains(page, `id="matrix-bulk"`) || !strings.Contains(page, `class="row-tick"`) {
		t.Fatal("no ticks on the matrix")
	}
	svcs, _ := e.st.ListServices(context.Background(), e.ws.Scope())
	_, body, _ := post(t, member, e.srv.URL+"/services/team", url.Values{"service": {svcs[0].Name, "no-such"}, "team": {"team-pay"}, "return": {"/?group=team"}})
	if !strings.Contains(body, "1 service belongs to team-pay now.") {
		t.Fatalf("bulk team: %s", body[:min(len(body), 300)])
	}
	if got, _ := e.st.GetServiceByName(context.Background(), e.ws.Scope(), svcs[0].Name); got.Owner != "team-pay" {
		t.Errorf("owner %q", got.Owner)
	}
	if _, page = get(t, e.as(store.RoleViewer), e.srv.URL+"/", nil); strings.Contains(page, `class="row-tick"`) {
		t.Error("a viewer gets ticks")
	}
}
