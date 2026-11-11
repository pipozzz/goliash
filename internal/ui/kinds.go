// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/pipozzz/goliash/internal/versions"
)

// A kind is where a service runs, from its targets' platform: what the matrix and the tiles show as an icon and
// filter by (type:lambda in the filter box).
type kind struct {
	Key, Label string
	Icon       string // SVG path data on a 16×16 grid, stroked
}

const (
	iconWheel     = "M8 1.6l5 2.4 1.2 5.4-3.5 4.3H5.3L1.8 9.4 3 4zM8 6.2a1.8 1.8 0 1 0 0 3.6 1.8 1.8 0 0 0 0-3.6z"
	iconContainer = "M8 1.8l5.5 3v6.4L8 14.2l-5.5-3V4.8zM2.5 4.8 8 7.8l5.5-3M8 7.8v6.4"
)

// kinds in the order the filter offers them.
var kinds = []kind{
	{"kubernetes", "Kubernetes", iconWheel},
	{"eks", "EKS", iconWheel},
	{"ecs", "ECS", "M2 2.5h5v5H2zM9 2.5h5v5H9zM5.5 8.5h5v5h-5z"},
	{"lambda", "Lambda", "M3.5 2.5h3l6 11M8.6 7.6 4 13.5"},
	{"docker", "Docker", iconContainer},
	{"compose", "Compose", iconContainer},
	{"swarm", "Swarm", iconContainer},
	{"nomad", "Nomad", "M8 1.5l5.6 3.2v6.6L8 14.5l-5.6-3.2V4.7zM5.8 10.5v-5l4.4 5v-5"},
	{"rds", "RDS", "M3 4c0-1.1 2.2-2 5-2s5 .9 5 2-2.2 2-5 2-5-.9-5-2zm0 0v8c0 1.1 2.2 2 5 2s5-.9 5-2V4M3 8c0 1.1 2.2 2 5 2s5-.9 5-2"},
	{"elasticache", "ElastiCache", "M9 1.5 3.5 9h4L7 14.5 12.5 7h-4z"},
	{"opensearch", "OpenSearch", "M7 2.5a4.5 4.5 0 1 0 0 9 4.5 4.5 0 0 0 0-9zM10.3 10.3l3.2 3.2"},
	{"msk", "MSK", "M2.5 4.5h11M2.5 8h11M2.5 11.5h11"},
	{"host", "Linux host", "M2.5 2.5h11v4.5h-11zM2.5 9h11v4.5h-11zM5 4.75h.01M5 11.25h.01"},
	{"manual", "By hand", "M10.5 2.5l3 3L6 13H3v-3z"},
}

func kindOf(key string) (kind, bool) {
	for _, k := range kinds {
		if k.Key == key {
			return k, true
		}
	}
	return kind{}, false
}

// rowKinds are the kinds of the targets a row's versions run on, in the filter's order.
func rowKinds(o versions.Overview, targetIDs []string) []string {
	var out []string
	for _, t := range o.Targets {
		if slices.Contains(targetIDs, t.ID) && !slices.Contains(out, t.Platform) {
			if _, ok := kindOf(t.Platform); ok {
				out = append(out, t.Platform)
			}
		}
	}
	return sortKinds(out)
}

// sortKinds orders kinds as the filter offers them.
func sortKinds(ks []string) []string {
	slices.SortFunc(ks, func(a, b string) int {
		return slices.IndexFunc(kinds, func(k kind) bool { return k.Key == a }) - slices.IndexFunc(kinds, func(k kind) bool { return k.Key == b })
	})
	return ks
}

// kindTokens are the words the filter box matches a row's kinds by.
func kindTokens(ks []string) string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = "type:" + k
	}
	return strings.Join(out, " ")
}

// kindOptions are the kinds present among rows, for the filter's buttons; none when there is only one.
func kindOptions(rows []MatrixRow) []kind {
	var keys []string
	for _, r := range rows {
		for _, k := range r.Kinds {
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	if len(keys) < 2 {
		return nil
	}
	var out []kind
	for _, k := range sortKinds(keys) {
		kd, _ := kindOf(k)
		out = append(out, kd)
	}
	return out
}

// kindGroups name the matrix's groups when it is grouped by kind: what something is, more than where it runs.
var kindGroups = map[string]string{
	"kubernetes": "Kubernetes workloads", "ecs": "Containers", "docker": "Containers", "compose": "Containers",
	"swarm": "Containers", "nomad": "Containers", "lambda": "Functions", "rds": "Databases", "elasticache": "Caches",
	"opensearch": "Search", "msk": "Streaming", "eks": "Kubernetes clusters", "host": "Hosts", "manual": "Added by hand",
}

// kindGroupOrder orders the groups: what teams deploy first, then what AWS runs for them.
var kindGroupOrder = []string{
	"Kubernetes workloads", "Containers", "Functions", "Databases", "Caches", "Search", "Streaming",
	"Kubernetes clusters", "Hosts", "Added by hand",
}

// kindGroup is the group of a row grouped by kind: its first kind's.
func kindGroup(r MatrixRow) string {
	if len(r.Kinds) == 0 {
		return "not running"
	}
	return kindGroups[r.Kinds[0]]
}

// kindGroupIcon is the icon of a kind group: its first kind's.
func kindGroupIcon(group string) string {
	for _, k := range kinds {
		if kindGroups[k.Key] == group {
			return k.Key
		}
	}
	return ""
}

// engineNames are how people call the engines of managed services and Lambda runtimes.
var engineNames = map[string]string{
	"postgres": "PostgreSQL", "mysql": "MySQL", "mariadb": "MariaDB", "aurora-postgresql": "Aurora PostgreSQL",
	"aurora-mysql": "Aurora MySQL", "redis": "Redis OSS", "valkey": "Valkey", "memcached": "Memcached",
	"kubernetes": "Kubernetes", "opensearch": "OpenSearch", "elasticsearch": "Elasticsearch", "kafka": "Kafka",
	"python": "Python", "nodejs": "Node.js", "java": "Java", "dotnet": ".NET", "ruby": "Ruby", "go": "Go",
}

// rowWhat says what a row is when its name does not: the engine of a managed database or cache, or the runtime of
// a .zip Lambda function ("PostgreSQL · RDS", "Node.js runtime"). Empty for images, whose name says it.
func rowWhat(repo string) string {
	if family := versions.LambdaRuntime(repo); family != "" {
		return engineNames[family] + " runtime"
	}
	if !versions.Managed(repo) {
		return ""
	}
	host, engine, _ := strings.Cut(repo, "/")
	service := map[string]string{
		"rds.amazonaws.com": "RDS", "elasticache.amazonaws.com": "ElastiCache", "eks.amazonaws.com": "EKS",
		"opensearch.amazonaws.com": "OpenSearch Service", "kafka.amazonaws.com": "MSK",
	}[host]
	name := engineNames[engine]
	if name == "" {
		name = engine
	}
	switch {
	case service == "" || service == name:
		return name
	case strings.HasPrefix(service, name):
		return service // "OpenSearch Service", not "OpenSearch · OpenSearch Service"
	}
	return name + " · " + service
}

// serviceKinds are the kinds each service runs on, by service ID, from the matrix.
func serviceKinds(o versions.Overview) map[string][]string {
	out := map[string][]string{}
	for _, row := range o.Matrix.Rows {
		var ids []string
		cells := row.Cells
		for _, part := range row.Parts {
			cells = append(cells, part.Cells...)
		}
		for _, c := range cells {
			for _, v := range c.Versions {
				ids = append(ids, v.TargetIDs...)
			}
		}
		out[row.Service.ID] = rowKinds(o, ids)
	}
	return out
}

// AWSUpgrade is how to move a managed service or a .zip function to the newest version in one environment.
type AWSUpgrade struct {
	Env, From, To, Command string
}

// awsUpgradeCommand is the AWS CLI command that upgrades the engine of a managed service, or the runtime of a .zip
// function, named name in region, from current to target; empty for anything else. The name is the service's: for a
// resource named otherwise, it is edited before running.
func awsUpgradeCommand(repo, name, region, current, target string) string {
	reg := ""
	if region != "" {
		reg = " --region " + region
	}
	if family := versions.LambdaRuntime(repo); family != "" {
		runtime := map[string]string{
			"nodejs": "nodejs" + target + ".x", "python": "python" + target, "java": "java" + target,
			"dotnet": "dotnet" + target, "ruby": "ruby" + target, "go": "provided.al2023",
		}[family]
		return "aws lambda update-function-configuration --function-name " + name + " --runtime " + runtime + reg
	}
	host, engine, _ := strings.Cut(repo, "/")
	major := func(v string) string { m, _, _ := strings.Cut(v, "."); return m }
	allowMajor := ""
	if major(current) != major(target) {
		allowMajor = " --allow-major-version-upgrade"
	}
	switch host {
	case "rds.amazonaws.com":
		if strings.HasPrefix(engine, "aurora") {
			return "aws rds modify-db-cluster --db-cluster-identifier " + name + " --engine-version " + target + allowMajor + " --apply-immediately" + reg
		}
		return "aws rds modify-db-instance --db-instance-identifier " + name + " --engine-version " + target + allowMajor + " --apply-immediately" + reg
	case "elasticache.amazonaws.com":
		return "aws elasticache modify-replication-group --replication-group-id " + name + " --engine-version " + target + " --apply-immediately" + reg
	case "eks.amazonaws.com":
		return "aws eks update-cluster-version --name " + name + " --kubernetes-version " + target + reg
	case "opensearch.amazonaws.com":
		prefix := "OpenSearch_"
		if engine == "elasticsearch" {
			prefix = "Elasticsearch_"
		}
		return "aws opensearch upgrade-domain --domain-name " + name + " --target-version " + prefix + target + reg
	case "kafka.amazonaws.com":
		return "aws kafka update-cluster-kafka-version --cluster-arn <arn of " + name + "> --current-version <cluster version> --target-kafka-version " + target + reg
	}
	return ""
}

// targetRegion reads the AWS region from a target's settings ({"rds":{"region":"eu-west-1"}}); empty for none.
func targetRegion(settings []byte) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(settings, &m) != nil {
		return ""
	}
	for _, raw := range m {
		var s struct {
			Region string `json:"region"`
		}
		if json.Unmarshal(raw, &s) == nil && s.Region != "" {
			return s.Region
		}
	}
	return ""
}

// awsRunKind says who runs what: a managed engine is AWS's, a .zip function is your code on AWS's runtime.
func awsRunKind(repo string) string {
	if versions.LambdaRuntime(repo) != "" {
		return "your code on an AWS runtime"
	}
	return "run by AWS"
}

// matrixTeams are the teams the matrix's services belong to, for the bulk team field.
func matrixTeams(rows []MatrixRow) []string {
	var out []string
	for _, r := range rows {
		if r.Owner != "" && !slices.Contains(out, r.Owner) {
			out = append(out, r.Owner)
		}
	}
	slices.Sort(out)
	return out
}

// fixAdvice says how a fixed package gets into a running image: an own image is rebuilt on its patched base, a
// third-party one needs a release that has it.
func fixAdvice(kind string) string {
	if kind == "third_party" {
		return "Move to an upstream release built with it (the Upstream releases panel), or ask its maintainers for one."
	}
	return "Rebuild the image so it pulls the patched package (update its base image or run the package upgrade in the build), then deploy it."
}

// mitigationPlaces names where a vulnerability runs without a mitigation.
func mitigationPlaces(ps []MitigationPlace) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Label
	}
	return strings.Join(out, ", ")
}
