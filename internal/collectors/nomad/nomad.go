// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package nomad

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// New is the collectors.Factory for Nomad targets. The ACL token (list-jobs and
// read-job are enough) comes from the target's credentials_ref, or NOMAD_TOKEN when none is set.
func New(_ context.Context, t agentproto.Target) (collectors.Collector, error) {
	if t.Nomad == nil || t.Nomad.Address == "" {
		return nil, errors.New("nomad target has no address")
	}
	token := os.Getenv("NOMAD_TOKEN")
	if t.CredentialsRef != nil && *t.CredentialsRef != "" {
		var err error
		if token, err = collectors.Credential(*t.CredentialsRef); err != nil {
			return nil, err
		}
	}
	c := &Collector{
		address: strings.TrimSuffix(t.Nomad.Address, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	if t.Nomad.Region != nil {
		c.region = *t.Nomad.Region
	}
	c.namespaces = t.Nomad.Namespaces
	return c, nil
}

// Collector reads Nomad jobs and the images of their running allocations.
type Collector struct {
	address    string
	token      string
	region     string
	namespaces []string
	http       *http.Client
}

type jobStub struct {
	ID               string
	Namespace        string
	Type             string
	Status           string
	ParentID         string // set on each run of a periodic or parameterized job
	Periodic         bool
	ParameterizedJob bool
}

type job struct {
	ID         string
	Namespace  string
	Version    uint64
	Meta       map[string]string
	TaskGroups []struct {
		Name  string
		Count int
		Meta  map[string]string
		Tasks []struct {
			Name   string
			Driver string
			Config map[string]any
			Meta   map[string]string
		}
	}
}

// labels are a job's meta as its tasks see it: the job's, then a group's, then a
// task's, the more specific winning, as Nomad merges them. The workload is the whole
// job, so a group or task key counts only when every group or task setting it agrees.
func (j job) labels() map[string]string {
	out := map[string]string{}
	for k, v := range j.Meta {
		out[k] = v
	}
	overlay := func(metas []map[string]string) {
		agreed := map[string]string{}
		mixed := map[string]bool{}
		for _, m := range metas {
			for k, v := range m {
				if old, ok := agreed[k]; ok && old != v {
					mixed[k] = true
				}
				agreed[k] = v
			}
		}
		for k, v := range agreed {
			if !mixed[k] {
				out[k] = v
			}
		}
	}
	var groups, tasks []map[string]string
	for _, g := range j.TaskGroups {
		groups = append(groups, g.Meta)
		for _, t := range g.Tasks {
			tasks = append(tasks, t.Meta)
		}
	}
	overlay(groups)
	overlay(tasks)
	if len(out) == 0 {
		return nil
	}
	return out
}

type allocStub struct {
	ID           string
	TaskGroup    string
	JobVersion   uint64
	ClientStatus string
}

// Collect lists jobs and, per job, counts running allocations by the image of
// the job version they run. During a deployment old and new versions show side by side.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	namespaces := c.namespaces
	if len(namespaces) == 0 {
		namespaces = []string{"*"}
	}
	var stubs []jobStub
	for _, ns := range namespaces {
		var page []jobStub
		if err := c.get(ctx, "/v1/jobs", url.Values{"namespace": {ns}}, &page); err != nil {
			return collectors.Result{}, err
		}
		stubs = append(stubs, page...)
	}
	sort.Slice(stubs, func(i, j int) bool {
		if stubs[i].Namespace != stubs[j].Namespace {
			return stubs[i].Namespace < stubs[j].Namespace
		}
		return stubs[i].ID < stubs[j].ID
	})

	res := collectors.Result{Complete: true, Workloads: []agentproto.Workload{}}
	// Each run of a periodic or parameterized job is a job of its own
	// ("backup/periodic-1759740000"); they count as the parent's, one workload that
	// keeps its name, instead of a new workload per run.
	children := map[string][]jobStub{}
	for _, s := range stubs {
		if s.ParentID != "" && s.Status != "dead" {
			k := s.Namespace + "/" + s.ParentID
			children[k] = append(children[k], s)
		}
	}
	byID := map[string]int{}
	for _, s := range stubs {
		if s.Status == "dead" || s.ParentID != "" {
			continue
		}
		w, err := c.workload(ctx, s)
		if err != nil {
			// One unreadable job should not hide the others.
			res.Complete = false
			res.Errors = append(res.Errors, fmt.Sprintf("job %s/%s: %v", s.Namespace, s.ID, err))
			continue
		}
		if w != nil {
			if s.Periodic || s.ParameterizedJob {
				w.Kind, w.DesiredReplicas = agentproto.Cronjob, nil
			}
			byID[w.ID] = len(res.Workloads)
			res.Workloads = append(res.Workloads, *w)
		}
	}
	parents := make([]string, 0, len(children))
	for k := range children {
		parents = append(parents, k)
	}
	sort.Strings(parents)
	for _, parent := range parents {
		for _, s := range children[parent] {
			run, err := c.workload(ctx, s)
			if err != nil {
				res.Complete = false
				res.Errors = append(res.Errors, fmt.Sprintf("job %s/%s: %v", s.Namespace, s.ID, err))
				continue
			}
			if run == nil {
				continue
			}
			i, ok := byID[parent]
			if !ok { // the parent was not listed (dead, or another namespace): the run stands for it
				_, name, _ := strings.Cut(parent, "/")
				run.ID, run.Name, run.Kind, run.DesiredReplicas = parent, name, agentproto.Cronjob, nil
				byID[parent] = len(res.Workloads)
				res.Workloads = append(res.Workloads, *run)
				continue
			}
			res.Workloads[i].Containers = addRunning(res.Workloads[i].Containers, run.Containers)
		}
	}
	return res, nil
}

// addRunning adds a run's running containers to its parent's: the same container and
// image counts up, another image (a newer job version) is listed beside it.
func addRunning(into, run []agentproto.Container) []agentproto.Container {
	for _, c := range run {
		if c.Running == 0 {
			continue
		}
		found := false
		for i := range into {
			if into[i].Name == c.Name && into[i].Image == c.Image {
				into[i].Running += c.Running
				found = true
				break
			}
		}
		if !found {
			into = append(into, c)
		}
	}
	return into
}

func (c *Collector) workload(ctx context.Context, s jobStub) (*agentproto.Workload, error) {
	q := url.Values{"namespace": {s.Namespace}}
	path := "/v1/job/" + url.PathEscape(s.ID)

	// The job as it is now; older versions only when an allocation still runs one (a
	// deployment under way), as the history holds whole job specifications.
	var current job
	if err := c.get(ctx, path, q, &current); err != nil {
		return nil, err
	}
	byVersion := map[uint64]job{current.Version: current}
	var allocs []allocStub
	if err := c.get(ctx, path+"/allocations", q, &allocs); err != nil {
		return nil, err
	}
	for _, a := range allocs {
		if _, ok := byVersion[a.JobVersion]; ok || a.ClientStatus != "running" {
			continue
		}
		var versions struct{ Versions []job }
		if err := c.get(ctx, path+"/versions", q, &versions); err != nil {
			return nil, err
		}
		for _, v := range versions.Versions {
			if _, ok := byVersion[v.Version]; !ok {
				byVersion[v.Version] = v
			}
		}
		break
	}

	ns := s.Namespace
	w := &agentproto.Workload{
		ID: s.Namespace + "/" + s.ID, Kind: agentproto.NomadJob, Namespace: &ns, Name: s.ID, Labels: current.labels(),
	}
	desired := 0
	for _, g := range current.TaskGroups {
		desired += g.Count
	}
	// Only a service job keeps its allocations running. A system job runs one per node
	// (its count says nothing), and a batch job's allocations finish: none running is
	// how it ends, not a service down.
	if s.Type == "service" || s.Type == "" {
		w.DesiredReplicas = &desired
	}

	type key struct{ name, image string }
	counts := map[key]int{}
	for _, a := range allocs {
		if a.ClientStatus != "running" {
			continue
		}
		// Nomad keeps only the last few versions of a job (JobTrackedVersions, 6 by
		// default). An allocation still running an older one is read with its own copy of
		// the job, or it would count as nothing running.
		if _, ok := byVersion[a.JobVersion]; !ok && a.ID != "" {
			var alloc struct{ Job *job }
			if err := c.get(ctx, "/v1/allocation/"+url.PathEscape(a.ID), url.Values{"namespace": {s.Namespace}}, &alloc); err == nil && alloc.Job != nil {
				byVersion[a.JobVersion] = *alloc.Job
			}
		}
		for _, t := range tasks(byVersion[a.JobVersion], a.TaskGroup) {
			counts[t]++
		}
	}
	seen := map[string]bool{}
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].name != keys[j].name {
			return keys[i].name < keys[j].name
		}
		return keys[i].image < keys[j].image
	})
	for _, k := range keys {
		image, digest := collectors.SplitImage(k.image)
		ct := agentproto.Container{Name: k.name, Image: image, Running: counts[k]}
		if digest != "" {
			ct.Digest = &digest
		}
		w.Containers = append(w.Containers, ct)
		seen[k.name] = true
	}
	for _, g := range current.TaskGroups {
		for _, t := range tasks(current, g.Name) {
			if !seen[t.name] {
				image, digest := collectors.SplitImage(t.image)
				ct := agentproto.Container{Name: t.name, Image: image, Running: 0}
				if digest != "" {
					ct.Digest = &digest
				}
				w.Containers = append(w.Containers, ct)
				seen[t.name] = true
			}
		}
	}
	if len(w.Containers) == 0 {
		return nil, nil // no container tasks (exec, raw_exec, java)
	}
	return w, nil
}

// tasks returns "group/task" names and images of the container tasks of one group.
func tasks(j job, group string) []struct{ name, image string } {
	var out []struct{ name, image string }
	for _, g := range j.TaskGroups {
		if g.Name != group {
			continue
		}
		for _, t := range g.Tasks {
			image, _ := t.Config["image"].(string)
			if image == "" || (t.Driver != "docker" && t.Driver != "podman" && t.Driver != "containerd-driver") {
				continue
			}
			image = interpolate(image, j, g.Name, t.Name, g.Meta, t.Meta)
			out = append(out, struct{ name, image string }{g.Name + "/" + t.Name, image})
		}
	}
	return out
}

// nomadVar matches ${...} in a job specification.
var nomadVar = regexp.MustCompile(`\$\{([^}]+)\}`)

// interpolate resolves the variables Nomad itself would in a task's image, as the API
// returns the specification uninterpolated: image = "app:${NOMAD_META_version}" is
// common. Meta (${NOMAD_META_x}, ${meta.x}) merges job, group and task, the most
// specific winning; the job, group and task names are known too. Variables only a
// client knows (${attr.*}, ${node.*}) stay as they are.
func interpolate(s string, j job, group, task string, groupMeta, taskMeta map[string]string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	meta := map[string]string{}
	for _, m := range []map[string]string{j.Meta, groupMeta, taskMeta} {
		for k, v := range m {
			meta[k] = v
		}
	}
	env := func(k string) string { // a key as an environment variable name: NOMAD_META_<key>
		return strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
				return r
			}
			return '_'
		}, k)
	}
	return nomadVar.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.TrimSpace(m[2 : len(m)-1])
		switch name {
		case "NOMAD_JOB_NAME", "NOMAD_JOB_ID":
			return j.ID
		case "NOMAD_GROUP_NAME":
			return group
		case "NOMAD_TASK_NAME":
			return task
		case "NOMAD_NAMESPACE":
			return j.Namespace
		}
		key, ok := strings.CutPrefix(name, "NOMAD_META_")
		if !ok {
			if key, ok = strings.CutPrefix(name, "meta."); !ok {
				return m
			}
			if v, found := meta[key]; found {
				return v
			}
			return m
		}
		for k, v := range meta {
			if e := env(k); e == key || strings.ToUpper(e) == key {
				return v
			}
		}
		return m
	})
}

func (c *Collector) get(ctx context.Context, path string, q url.Values, v any) error {
	if c.region != "" {
		q.Set("region", c.region)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.address+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("X-Nomad-Token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("nomad API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		err := fmt.Errorf("nomad API GET %s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
		if resp.StatusCode == http.StatusForbidden {
			if c.token == "" {
				return fmt.Errorf("%w (no ACL token: set credentials_ref or NOMAD_TOKEN)", err)
			}
			return fmt.Errorf("%w (the ACL token needs list-jobs and read-job)", err)
		}
		return err
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
