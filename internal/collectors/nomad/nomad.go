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
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// New is the collectors.Factory for Nomad targets. The ACL token (read-job is
// enough) comes from the target's credentials_ref, or NOMAD_TOKEN when none is set.
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
	ID        string
	Namespace string
	Type      string
	Status    string
}

type job struct {
	ID         string
	Namespace  string
	Version    uint64
	Meta       map[string]string
	TaskGroups []struct {
		Name  string
		Count int
		Tasks []struct {
			Name   string
			Driver string
			Config map[string]any
		}
	}
}

type allocStub struct {
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
	for _, s := range stubs {
		if s.Status == "dead" {
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
			res.Workloads = append(res.Workloads, *w)
		}
	}
	return res, nil
}

func (c *Collector) workload(ctx context.Context, s jobStub) (*agentproto.Workload, error) {
	q := url.Values{"namespace": {s.Namespace}}
	path := "/v1/job/" + url.PathEscape(s.ID)

	var versions struct{ Versions []job }
	if err := c.get(ctx, path+"/versions", q, &versions); err != nil {
		return nil, err
	}
	if len(versions.Versions) == 0 {
		return nil, nil
	}
	byVersion := map[uint64]job{}
	current := versions.Versions[0]
	for _, v := range versions.Versions {
		byVersion[v.Version] = v
		if v.Version > current.Version {
			current = v
		}
	}
	var allocs []allocStub
	if err := c.get(ctx, path+"/allocations", q, &allocs); err != nil {
		return nil, err
	}

	ns := s.Namespace
	w := &agentproto.Workload{
		ID: s.Namespace + "/" + s.ID, Kind: agentproto.NomadJob, Namespace: &ns, Name: s.ID, Labels: current.Meta,
	}
	desired := 0
	for _, g := range current.TaskGroups {
		desired += g.Count
	}
	if s.Type != "system" && s.Type != "sysbatch" {
		w.DesiredReplicas = &desired
	}

	type key struct{ name, image string }
	counts := map[key]int{}
	for _, a := range allocs {
		if a.ClientStatus != "running" {
			continue
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
			out = append(out, struct{ name, image string }{g.Name + "/" + t.Name, image})
		}
	}
	return out
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
		return fmt.Errorf("nomad API GET %s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
