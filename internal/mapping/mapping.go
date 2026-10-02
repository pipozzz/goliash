// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package mapping

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// Labels with a meaning to Goliash. They are Kubernetes labels, ECS tags, Nomad meta or Swarm labels.
const (
	LabelService   = "goliash.service"   // service name; wins over everything else
	LabelEnv       = "goliash.env"       // environment name, overriding the target's
	LabelContainer = "goliash.container" // name of the main container of a multi-container workload
	labelK8sName   = "app.kubernetes.io/name"
)

// knownSidecars are never picked as the main container on their own.
var knownSidecars = []string{
	"istio-proxy", "linkerd-proxy", "envoy", "datadog-agent", "vault-agent", "cloud-sql-proxy",
	"fluent-bit", "fluentd", "otel-collector", "aws-otel-collector", "xray-daemon", "log-router", "oauth2-proxy",
}

// Workload is what mapping looks at.
type Workload struct {
	Name   string
	Labels map[string]string
}

// Decision is how one container of a workload is classified.
type Decision struct {
	Ignore      bool
	ServiceName string // set by a label: the service is created when missing
	ServiceID   string // set by a rule
	Suggested   string // heuristic proposal for the inbox when nothing else matched
	EnvName     string // from goliash.env; empty means the target's environment
	Source      string // label, rule or heuristic
}

type rule struct {
	store.MappingRule
	re       *regexp.Regexp
	labelKey string
}

// Mapper applies the mapping layers: labels, then rules, then a heuristic.
type Mapper struct {
	rules []rule
}

// New compiles the rules. Rules that do not compile are skipped and returned as errors.
func New(rules []store.MappingRule) (*Mapper, []error) {
	m := &Mapper{}
	var errs []error
	for _, r := range rules {
		pattern := r.Pattern
		cr := rule{MappingRule: r}
		if r.MatchType == "label" {
			key, value, ok := strings.Cut(r.Pattern, "=")
			if !ok || key == "" {
				errs = append(errs, fmt.Errorf("rule %s: label pattern must be key=regexp", r.ID))
				continue
			}
			cr.labelKey, pattern = key, value
		}
		re, err := regexp.Compile("^(?:" + pattern + ")$")
		if err != nil {
			errs = append(errs, fmt.Errorf("rule %s: %w", r.ID, err))
			continue
		}
		cr.re = re
		m.rules = append(m.rules, cr)
	}
	sort.SliceStable(m.rules, func(i, j int) bool { return m.rules[i].Priority < m.rules[j].Priority })
	return m, errs
}

// Map classifies one container of a workload.
func (m *Mapper) Map(w Workload, container string, image versions.ImageRef) Decision {
	d := Decision{EnvName: w.Labels[LabelEnv]}

	for _, r := range m.rules {
		if r.MatchType == "ignore" && (r.re.MatchString(image.Repo()) || r.re.MatchString(container)) {
			return Decision{Ignore: true, Source: "rule"}
		}
	}
	if name := strings.TrimSpace(w.Labels[LabelService]); name != "" {
		d.ServiceName, d.Source = name, "label"
		return d
	}
	if name := strings.TrimSpace(w.Labels[labelK8sName]); name != "" {
		d.ServiceName, d.Source = name, "label"
		return d
	}
	for _, r := range m.rules {
		var match bool
		switch r.MatchType {
		case "image_repo":
			match = r.re.MatchString(image.Repo())
		case "workload_name":
			match = r.re.MatchString(w.Name)
		case "label":
			v, ok := w.Labels[r.labelKey]
			match = ok && r.re.MatchString(v)
		}
		if match && r.ServiceID != "" {
			d.ServiceID, d.Source = r.ServiceID, "rule"
			return d
		}
	}
	d.Suggested, d.Source = image.Name(), "heuristic"
	return d
}

// MainContainer picks the container that carries the workload's version: the one
// named by goliash.container, else one named like the workload or service, else the
// only container that is not a known sidecar, else the first such by name.
func MainContainer(w Workload, containers []string, service string) string {
	if want := w.Labels[LabelContainer]; want != "" {
		for _, c := range containers {
			if c == want {
				return c
			}
		}
	}
	var candidates []string
	for _, c := range containers {
		if !isSidecar(c) {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		candidates = containers
	}
	sort.Strings(candidates)
	for _, c := range candidates {
		base := c[strings.LastIndex(c, "/")+1:] // Nomad: group/task
		if base == w.Name || (service != "" && base == service) || c == w.Name {
			return c
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

func isSidecar(container string) bool {
	base := container[strings.LastIndex(container, "/")+1:]
	for _, s := range knownSidecars {
		if base == s {
			return true
		}
	}
	return false
}
