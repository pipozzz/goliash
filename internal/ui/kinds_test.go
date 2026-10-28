// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"strings"
	"testing"

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
