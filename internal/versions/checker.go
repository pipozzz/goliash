// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// maxReleases is how many of the newest acceptable tags are kept per check.
const maxReleases = 30

// publicRegistries are checked by the server. Anything else is assumed private and
// checked by the agent, whose credentials never leave the customer network.
var publicRegistries = map[string]bool{
	"docker.io": true, "ghcr.io": true, "quay.io": true, "registry.k8s.io": true, "gcr.io": true,
	"public.ecr.aws": true, "mcr.microsoft.com": true, "registry.gitlab.com": true, "docker.elastic.co": true,
}

// IsPublicRegistry reports whether the server checks repo itself.
func IsPublicRegistry(repo string) bool {
	host, _, _ := strings.Cut(repo, "/")
	return publicRegistries[host]
}

// TagLister lists the tags of an image repository.
type TagLister interface {
	ListTags(ctx context.Context, repository string, creds registry.Credentials) ([]string, error)
}

// SourceReader reads the source repository an image declares. registry.Client
// implements it; tag listers without it skip source discovery.
type SourceReader interface {
	ImageSource(ctx context.Context, repository, reference string, creds registry.Credentials) (string, error)
}

// sourceTTL is how long a source read from an image is trusted before it is read again.
const sourceTTL = 7 * 24 * time.Hour

// Checker compares running versions with upstream registries and keeps drift up to date.
type Checker struct {
	store    *store.Store
	tags     TagLister
	log      *slog.Logger
	ttl      time.Duration
	onEvents func(store.Scope, []store.Event)

	mu     sync.Mutex
	cache  map[string]cachedTags
	now    func() time.Time
	github *GitHub
	gitlab *GitLab
	eol    *EOL

	driftMu sync.Mutex // one drift evaluation at a time
}

type cachedTags struct {
	tags []string
	err  error
	at   time.Time
}

// NewChecker returns a checker. Tag lists are cached for ttl, which keeps Docker Hub
// rate limits at bay when many services share an image.
func NewChecker(st *store.Store, tags TagLister, log *slog.Logger, ttl time.Duration) *Checker {
	return &Checker{
		store: st, tags: tags, log: log, ttl: ttl, cache: map[string]cachedTags{},
		now: func() time.Time { return time.Now().UTC() },
	}
}

// OnEvents registers a callback for new_release and drift events.
func (c *Checker) OnEvents(fn func(store.Scope, []store.Event)) { c.onEvents = fn }

// Run checks upstreams every interval and drift every driftInterval until ctx ends.
func (c *Checker) Run(ctx context.Context, interval, driftInterval time.Duration) {
	upstream := time.NewTicker(interval)
	defer upstream.Stop()
	drift := time.NewTicker(driftInterval)
	defer drift.Stop()
	c.runOnce(ctx, true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-upstream.C:
			c.runOnce(ctx, true)
		case <-drift.C:
			c.runOnce(ctx, false)
		}
	}
}

func (c *Checker) runOnce(ctx context.Context, upstream bool) {
	workspaces, err := c.store.ListWorkspaces(ctx)
	if err != nil {
		c.log.ErrorContext(ctx, "list workspaces", "err", err)
		return
	}
	for _, ws := range workspaces {
		// Every tick checks services seen for the first time; the full check runs on its interval.
		if err := c.checkUpstreams(ctx, ws.Scope(), !upstream); err != nil && ctx.Err() == nil {
			c.log.ErrorContext(ctx, "upstream check failed", "workspace", ws.Slug, "err", err)
		}
		if named, err := FillOwners(ctx, c.store, ws.Scope()); err != nil && ctx.Err() == nil {
			c.log.ErrorContext(ctx, "giving services their application's team failed", "workspace", ws.Slug, "err", err)
		} else if len(named) > 0 {
			c.log.InfoContext(ctx, "services got their application's team", "workspace", ws.Slug, "services", named)
		}
		if err := c.EvaluateDrift(ctx, ws.Scope()); err != nil && ctx.Err() == nil {
			c.log.ErrorContext(ctx, "drift evaluation failed", "workspace", ws.Slug, "err", err)
		}
	}
}

// workspaceState is what the checks read about a workspace.
type workspaceState struct {
	services  []store.Service
	byID      map[string]store.Service
	matrix    Matrix
	refs      map[string]Reference
	instances []store.Instance
}

// Reference is what a service's upstream is compared against: its image repository
// and the tag running in the highest environment.
type Reference struct {
	Repo string
	Tag  string
}

func (c *Checker) load(ctx context.Context, sc store.Scope) (workspaceState, error) {
	var st workspaceState
	var err error
	if st.services, err = c.store.ListServices(ctx, sc); err != nil {
		return st, err
	}
	envs, err := c.store.ListEnvironments(ctx, sc)
	if err != nil {
		return st, err
	}
	targets, err := c.store.ListTargets(ctx, sc)
	if err != nil {
		return st, err
	}
	if st.instances, err = c.store.ListActiveInstances(ctx, sc); err != nil {
		return st, err
	}
	if st.instances, err = namedInstances(ctx, c.store, sc, st.instances, st.services); err != nil {
		return st, err
	}
	st.matrix = BuildMatrix(st.services, envs, targets, st.instances)
	resolved, err := resolvedDigests(ctx, c.store, sc)
	if err != nil {
		return st, err
	}
	applyResolutions(&st.matrix, resolved)
	st.refs = References(st.matrix, st.instances)
	st.byID = map[string]store.Service{}
	for _, s := range st.services {
		st.byID[s.ID] = s
		// A service watched for its releases, running nowhere Goliash knows: check its upstream all the same.
		if _, ok := st.refs[s.ID]; !ok && s.Upstream != "" {
			st.refs[s.ID] = Reference{Repo: s.Upstream}
		}
	}
	return st, nil
}

// References picks, per service, the upstream repository (the service's configured
// upstream, else the image its main containers run) and the tag in the highest
// environment it runs in.
func References(m Matrix, active []store.Instance) map[string]Reference {
	repoCount := map[string]map[string]int{}
	for _, i := range active {
		if !i.IsMain || i.ServiceID == "" {
			continue
		}
		if repoCount[i.ServiceID] == nil {
			repoCount[i.ServiceID] = map[string]int{}
		}
		repoCount[i.ServiceID][ParseImage(i.Image).Repo()]++
	}
	refs := map[string]Reference{}
	for _, row := range m.Rows {
		var ref Reference
		for ei := len(row.Cells) - 1; ei >= 0; ei-- {
			if !row.Cells[ei].Empty() {
				ref.Tag = row.Cells[ei].Primary().Version()
				break
			}
		}
		ref.Repo = row.Service.Upstream
		if ref.Repo == "" {
			best := 0
			for repo, n := range repoCount[row.Service.ID] {
				if n > best || (n == best && repo < ref.Repo) {
					ref.Repo, best = repo, n
				}
			}
		}
		refs[row.Service.ID] = ref
	}
	return refs
}

// CheckUpstreams lists tags of public upstream repositories and records new releases.
// Private repositories are left to the agent.
func (c *Checker) CheckUpstreams(ctx context.Context, sc store.Scope) error {
	return c.checkUpstreams(ctx, sc, false)
}

func (c *Checker) checkUpstreams(ctx context.Context, sc store.Scope, onlyNew bool) error {
	st, err := c.load(ctx, sc)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(st.refs))
	for id := range st.refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ref := st.refs[id]
		if ref.Repo == "" || !serverChecks(st.byID[id], ref.Repo) {
			continue
		}
		if onlyNew {
			if checked, _, err := c.store.UpstreamStatus(ctx, sc, id); err != nil || !checked.IsZero() {
				continue
			}
		}
		tags, err := c.listTags(ctx, ref.Repo)
		if err != nil {
			if c.handOver(ctx, sc, st.byID[id], ref.Repo, err) {
				continue
			}
			c.log.WarnContext(ctx, "upstream check failed", "service", st.byID[id].Name, "repo", ref.Repo, "err", err)
			_ = c.store.RecordUpstreamCheck(ctx, sc, id, err.Error())
			continue
		}
		if c.resolveMoving(ctx, sc, st, id, ref.Repo, tags) {
			// The exact version behind a moving tag is the reference from now on.
			if fresh, err := c.load(ctx, sc); err == nil {
				st, ref = fresh, fresh.refs[id]
			}
		}
		svc := c.discoverSource(ctx, sc, st.byID[id], ref)
		evs, err := c.recordTags(ctx, sc, svc, ref.Repo, tags, ref.Tag)
		if err != nil {
			return err
		}
		c.emit(sc, evs)
	}
	return nil
}

// discoverSource reads the source repository ref's image declares, at most once per
// sourceTTL and again when the upstream image changes, and returns svc with it.
func (c *Checker) discoverSource(ctx context.Context, sc store.Scope, svc store.Service, ref Reference) store.Service {
	reader, ok := c.tags.(SourceReader)
	if !ok || (svc.SourceImage == ref.Repo && c.now().Sub(svc.SourceCheckedAt) < sourceTTL) {
		return svc
	}
	tag := ref.Tag
	if tag == "" {
		tag = "latest"
	}
	src, err := reader.ImageSource(ctx, ref.Repo, tag, registry.Credentials{})
	if err != nil {
		if ctx.Err() != nil {
			return svc
		}
		c.log.DebugContext(ctx, "image source not readable", "service", svc.Name, "repo", ref.Repo, "tag", tag, "err", err)
		if svc.SourceImage == ref.Repo {
			src = svc.SourceURL // keep what was known; try again after the TTL
		}
	}
	if err := c.store.SetServiceSource(ctx, sc, svc.ID, ref.Repo, src); err != nil {
		c.log.WarnContext(ctx, "record image source", "service", svc.Name, "err", err)
		return svc
	}
	if gh := LabelGitHub(store.Service{SourceURL: src, SourceImage: ref.Repo}, ref.Repo); gh != "" && src != svc.SourceURL {
		c.log.InfoContext(ctx, "release notes source found in image label", "service", svc.Name, "github", gh)
	}
	svc.SourceURL, svc.SourceImage, svc.SourceCheckedAt = src, ref.Repo, c.now()
	return svc
}

func (c *Checker) listTags(ctx context.Context, repo string) ([]string, error) {
	c.mu.Lock()
	if e, ok := c.cache[repo]; ok && time.Since(e.at) < c.ttl {
		c.mu.Unlock()
		return e.tags, e.err
	}
	c.mu.Unlock()
	if c.tags == nil {
		return nil, errors.New("no registry client")
	}
	tags, err := c.tags.ListTags(ctx, repo, registry.Credentials{})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	c.mu.Lock()
	c.cache[repo] = cachedTags{tags: tags, err: err, at: time.Now()}
	c.mu.Unlock()
	return tags, err
}

// recordTags stores the acceptable versions among tags as releases of svc, adds release
// dates and changelog links where the policy says they live, and returns a new_release
// event when a version newer than everything known before appears. The first check of
// a service is a baseline without events.
func (c *Checker) recordTags(ctx context.Context, sc store.Scope, svc store.Service, repo string, tags []string, running string) ([]store.Event, error) {
	st := c.store
	policy, _, err := PolicyFor(svc, repo)
	if err != nil {
		_ = st.RecordUpstreamCheck(ctx, sc, svc.ID, "version policy: "+err.Error())
		return nil, nil
	}
	candidates := Candidates(tags, running, policy)
	if len(candidates) > maxReleases {
		candidates = candidates[:maxReleases]
	}
	known, err := st.ListReleases(ctx, sc, svc.ID)
	if err != nil {
		return nil, err
	}
	var newestKnown *Version
	for _, r := range known {
		if v, ok := ParseVersion(r.Version); ok && (newestKnown == nil || v.Compare(*newestKnown) > 0) {
			newestKnown = &v
		}
	}
	raw := make([]string, len(candidates))
	for i, v := range candidates {
		raw[i] = v.Raw
	}
	added, err := st.InsertReleases(ctx, sc, svc.ID, raw)
	if err != nil {
		return nil, err
	}
	c.annotateReleases(ctx, sc, svc, policy)
	if err := st.RecordUpstreamCheck(ctx, sc, svc.ID, ""); err != nil {
		return nil, err
	}
	if len(known) == 0 || len(added) == 0 {
		return nil, nil
	}

	up := Latest(raw, running, policy)
	if !up.HasLatestAny || (newestKnown != nil && up.LatestAny.Compare(*newestKnown) <= 0) {
		return nil, nil
	}
	cur, _ := ParseVersion(running)
	note := string(cur.JumpTo(up.LatestAny))
	if policy.PinMajor != nil && up.LatestAny.part(0) != *policy.PinMajor {
		note = "outside pinned major"
	}
	evs := []store.Event{{
		Type: "new_release", ServiceID: svc.ID, FromVersion: running, ToVersion: up.LatestAny.Raw,
		Note: note, Source: "poll", At: time.Now().UTC(),
	}}
	return evs, persist(ctx, st, sc, evs)
}

func persist(ctx context.Context, st *store.Store, sc store.Scope, evs []store.Event) error {
	for _, e := range evs {
		if err := st.InsertEvent(ctx, sc, e); err != nil {
			return err
		}
	}
	return nil
}

func (c *Checker) emit(sc store.Scope, evs []store.Event) {
	for _, e := range evs {
		c.log.Info("event", "type", e.Type, "from", e.FromVersion, "to", e.ToVersion, "note", e.Note)
	}
	if c.onEvents != nil && len(evs) > 0 {
		c.onEvents(sc, evs)
	}
}

// DriftDetail is stored with a drift and shown to people.
type DriftDetail struct {
	Running string            `json:"running"`
	Other   string            `json:"other"`              // the newer version: upstream latest or the lower environment's
	OtherIn string            `json:"other_in,omitempty"` // environment name for env drift
	Jump    Jump              `json:"jump,omitempty"`
	Targets map[string]string `json:"targets,omitempty"` // inconsistent: target -> version
	EOL     string            `json:"eol,omitempty"`     // eol: the date the release cycle (Other) stops being supported
	On      []string          `json:"on,omitempty"`      // the targets running Running, when the environment runs other versions too
}

// WantedDrift is a drift the current state calls for.
type WantedDrift struct {
	Service, Env, Kind string
	Detail             DriftDetail
	App                string // the application, when the service splits by application
	TargetID           string // the one target the drift is about, when it is one of several in the environment
}

// EvaluateDrift opens drifts the current state shows and resolves those it no longer
// shows, recording drift_detected and drift_resolved events.
func (c *Checker) EvaluateDrift(ctx context.Context, sc store.Scope) error {
	c.driftMu.Lock()
	defer c.driftMu.Unlock()
	st, err := c.load(ctx, sc)
	if err != nil {
		return err
	}
	upstreams := map[string]Upstream{}
	policies := map[string]Policy{}
	for id, ref := range st.refs {
		p, _, err := PolicyFor(st.byID[id], ref.Repo)
		if err != nil {
			continue
		}
		policies[id] = p
		releases, err := c.store.ListReleases(ctx, sc, id)
		if err != nil {
			return err
		}
		tags := make([]string, len(releases))
		for i, r := range releases {
			tags[i] = r.Version
		}
		upstreams[id] = Latest(tags, ref.Tag, p)
	}
	wanted := Drifts(st.matrix, upstreams, policies)
	wanted = append(wanted, EOLDrifts(st.matrix, c.eolCycles(ctx, st, policies), c.now())...)

	open, err := c.store.OpenDrifts(ctx, sc)
	if err != nil {
		return err
	}
	key := func(service, app, env, kind string) string { return service + "|" + app + "|" + env + "|" + kind }
	openByKey := map[string]store.Drift{}
	for _, d := range open {
		openByKey[key(d.ServiceID, d.App, d.EnvironmentID, d.Kind)] = d
	}
	// An application renamed, merged or placed by hand changes drift keys only: an open
	// drift no longer wanted moves to the wanted one of the same service, environment
	// and kind, keeping its start and announcement, instead of resolving and reopening.
	wantedKeys := map[string]bool{}
	for _, w := range wanted {
		wantedKeys[key(w.Service, w.App, w.Env, w.Kind)] = true
	}
	orphans := map[string][]string{} // service|env|kind -> open drift keys no longer wanted
	for k, d := range openByKey {
		if !wantedKeys[k] {
			sk := d.ServiceID + "|" + d.EnvironmentID + "|" + d.Kind
			orphans[sk] = append(orphans[sk], k)
		}
	}
	for _, ks := range orphans {
		sort.Strings(ks)
	}
	moved := map[string]bool{} // service|env|kind whose drift moved to another application
	var evs []store.Event
	now := c.now()
	seen := map[string]bool{}
	for _, w := range wanted {
		k := key(w.Service, w.App, w.Env, w.Kind)
		seen[k] = true
		detail, _ := json.Marshal(w.Detail)
		d, ok := openByKey[k]
		if sk := w.Service + "|" + w.Env + "|" + w.Kind; !ok && len(orphans[sk]) > 0 {
			old := orphans[sk][0]
			orphans[sk] = orphans[sk][1:]
			d, ok = openByKey[old], true
			if err := c.store.SetDriftApp(ctx, sc, d.ID, w.App); err != nil {
				return err
			}
			seen[old] = true
			moved[sk] = true
			d.App = w.App
		}
		if ok {
			if string(d.Detail) != string(detail) {
				if err := c.store.UpdateDriftDetail(ctx, sc, d.ID, detail); err != nil {
					return err
				}
			}
		} else {
			var err error
			if d, err = c.store.OpenDrift(ctx, store.Drift{
				Scope: sc, ServiceID: w.Service, App: w.App, EnvironmentID: w.Env,
				Kind: w.Kind, Detail: detail, Since: now,
			}); err != nil {
				return err
			}
		}
		// Announce once the drift has lasted its alert delay.
		if d.NotifiedAt.IsZero() && now.Sub(d.Since) >= policies[w.Service].AlertAfter(w.Kind) {
			if err := c.store.MarkDriftNotified(ctx, sc, d.ID, now); err != nil {
				return err
			}
			evs = append(evs, store.Event{
				Type: "drift_detected", ServiceID: w.Service, App: w.App, EnvironmentID: w.Env,
				FromVersion: w.Detail.Running, ToVersion: w.Detail.Other, Note: w.Kind, Source: "poll", At: now,
				TargetID: w.TargetID,
			})
		}
	}
	for k, d := range openByKey {
		if seen[k] {
			continue
		}
		if err := c.store.ResolveDrift(ctx, sc, d.ID); err != nil {
			return err
		}
		if d.NotifiedAt.IsZero() {
			continue // never announced, so nothing to take back
		}
		if moved[d.ServiceID+"|"+d.EnvironmentID+"|"+d.Kind] {
			continue // merged into the drift that moved: it goes on there
		}
		var detail DriftDetail
		_ = json.Unmarshal(d.Detail, &detail)
		evs = append(evs, store.Event{
			Type: "drift_resolved", ServiceID: d.ServiceID, App: d.App, EnvironmentID: d.EnvironmentID,
			FromVersion: detail.Running, Note: d.Kind, Source: "poll", At: now,
		})
	}
	if err := persist(ctx, c.store, sc, evs); err != nil {
		return err
	}
	c.emit(sc, evs)
	return nil
}

// eolCycles looks up the release cycles of each service's product on endoflife.date:
// the policy's "eol" product, else the one the site lists for the upstream image.
func (c *Checker) eolCycles(ctx context.Context, st workspaceState, policies map[string]Policy) map[string][]EOLCycle {
	if c.eol == nil {
		return nil
	}
	out := map[string][]EOLCycle{}
	for id, ref := range st.refs {
		product := policies[id].EOL
		if product == "" && ref.Repo != "" {
			p, err := c.eol.Product(ctx, ref.Repo)
			if err != nil {
				c.log.DebugContext(ctx, "end-of-life products", "err", err)
				return out
			}
			product = p
		}
		if product == "" || product == "none" {
			continue
		}
		cycles, err := c.eol.Cycles(ctx, product)
		if err != nil {
			c.log.DebugContext(ctx, "end-of-life cycles", "product", product, "err", err)
			continue
		}
		out[id] = cycles
	}
	return out
}

// Drifts lists the drifts the matrix shows: an environment running an older version
// than the environment before it, a version behind upstream by at least the tracked
// jump, targets of one environment running different versions, and a running version
// that differs from what the Compose files in Git declare.
func Drifts(m Matrix, upstreams map[string]Upstream, policies map[string]Policy) []WantedDrift {
	var out []WantedDrift
	for _, row := range m.Rows {
		for _, part := range row.Units() {
			out = append(out, partDrifts(m, row.Service.ID, part, upstreams, policies)...)
		}
	}
	return out
}

// partDrifts lists the drifts of one service within one application (or all of it).
func partDrifts(m Matrix, svc string, part Part, upstreams map[string]Upstream, policies map[string]Policy) []WantedDrift {
	var out []WantedDrift
	add := func(env, kind string, d DriftDetail) {
		out = append(out, WantedDrift{Service: svc, Env: env, Kind: kind, Detail: d, App: part.App})
	}
	prevEnv := -1
	for ei, cell := range part.Cells {
		if cell.Empty() {
			continue
		}
		env := m.Environments[ei].ID
		primary := cell.Primary().Version()

		if prevEnv >= 0 {
			lower := part.Cells[prevEnv].Primary().Version()
			a, okA := ParseVersion(primary)
			b, okB := ParseVersion(lower)
			if okA && okB && a.Compare(b) < 0 {
				add(env, "env", DriftDetail{
					Running: primary, Other: lower, OtherIn: m.Environments[prevEnv].Name, Jump: a.JumpTo(b),
				})
			}
		}
		prevEnv = ei

		if up, ok := upstreams[svc]; ok {
			oldest := cell.Oldest()
			if jump, lag := Lagging(oldest.Version(), up, policies[svc]); lag {
				out = append(out, WantedDrift{
					Service: svc, Env: env, Kind: "upstream", App: part.App, TargetID: cell.OnlyTarget(oldest),
					Detail: DriftDetail{Running: oldest.Version(), Other: up.Latest.Raw, Jump: jump, On: cell.On(oldest)},
				})
			}
		}

		if targets, ok := inconsistent(cell); ok {
			add(env, "inconsistent", DriftDetail{Running: primary, Targets: targets})
		}

		// What runs differs from what the Compose files in Git declare.
		if !cell.FromDeclared && len(cell.Declared) > 0 && !hasTag(cell.Declared, primary) {
			add(env, "declared", DriftDetail{Running: primary, Other: cell.Declared[0].Tag})
		}
	}
	return out
}

func hasTag(vs []RunningVersion, tag string) bool {
	for _, v := range vs {
		if v.Tag == tag {
			return true
		}
	}
	return false
}

// inconsistent reports targets in one cell that run different versions with no
// overlap (a rollout inside one target is not drift).
func inconsistent(cell Cell) (map[string]string, bool) {
	if len(cell.Versions) < 2 {
		return nil, false
	}
	byTarget := map[string][]string{}
	for _, v := range cell.Versions {
		for _, t := range v.Targets {
			byTarget[t] = append(byTarget[t], v.Tag)
		}
	}
	if len(byTarget) < 2 {
		return nil, false
	}
	// Disjoint version sets between any two targets.
	names := make([]string, 0, len(byTarget))
	for t := range byTarget {
		names = append(names, t)
	}
	sort.Strings(names)
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if !overlap(byTarget[names[i]], byTarget[names[j]]) {
				out := map[string]string{}
				for _, t := range names {
					out[t] = strings.Join(byTarget[t], "+")
				}
				return out, true
			}
		}
	}
	return nil, false
}

func overlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// ErrNeedsCredentials means a public registry refused a repository anonymously; the
// agents check it with their credentials from now on.
var ErrNeedsCredentials = errors.New("needs credentials; the agents check it with theirs")

// ErrNoUpstream means a service has no image repository to check.
var ErrNoUpstream = errors.New("no upstream repository")

// CheckService checks one service's upstream immediately (CLI and UI "check now").
func (c *Checker) CheckService(ctx context.Context, sc store.Scope, serviceID string) error {
	st, err := c.load(ctx, sc)
	if err != nil {
		return err
	}
	ref, ok := st.refs[serviceID]
	if !ok || ref.Repo == "" {
		return ErrNoUpstream
	}
	if !IsPublicRegistry(ref.Repo) {
		return fmt.Errorf("%s is private; the agent checks it", ref.Repo)
	}
	// A repository on a public registry is tried anonymously again, in case it was
	// made public.
	c.mu.Lock()
	delete(c.cache, ref.Repo)
	c.mu.Unlock()
	svc := st.byID[serviceID]
	tags, err := c.listTags(ctx, ref.Repo)
	if err != nil {
		if c.handOver(ctx, sc, svc, ref.Repo, err) {
			return fmt.Errorf("%s: %w", ref.Repo, ErrNeedsCredentials)
		}
		_ = c.store.RecordUpstreamCheck(ctx, sc, serviceID, err.Error())
		return err
	}
	if svc.PrivateUpstream != "" {
		_ = c.store.SetPrivateUpstream(ctx, sc, serviceID, "")
	}
	if c.resolveMoving(ctx, sc, st, serviceID, ref.Repo, tags) {
		if fresh, err := c.load(ctx, sc); err == nil {
			st, ref = fresh, fresh.refs[serviceID]
		}
	}
	evs, err := c.recordTags(ctx, sc, st.byID[serviceID], ref.Repo, tags, ref.Tag)
	if err != nil {
		return err
	}
	c.emit(sc, evs)
	return c.EvaluateDrift(ctx, sc)
}

// serverChecks reports whether the server checks repo, svc's upstream, itself: it is
// on a public registry and the registry has not refused it anonymously.
func serverChecks(svc store.Service, repo string) bool {
	return IsPublicRegistry(repo) && svc.PrivateUpstream != repo
}

// handOver leaves repo to the agents when a public registry refused it anonymously
// (a private repository on GHCR or Docker Hub, say) and reports whether it did.
func (c *Checker) handOver(ctx context.Context, sc store.Scope, svc store.Service, repo string, err error) bool {
	if !errors.Is(err, registry.ErrUnauthorized) {
		return false
	}
	if svc.PrivateUpstream != repo {
		if err := c.store.SetPrivateUpstream(ctx, sc, svc.ID, repo); err != nil {
			c.log.WarnContext(ctx, "record private upstream", "service", svc.Name, "err", err)
			return false
		}
		c.log.InfoContext(ctx, "upstream needs credentials; the agents check it", "service", svc.Name, "repo", repo)
	}
	_ = c.store.RecordUpstreamCheck(ctx, sc, svc.ID, "")
	return true
}

// CheckedByAgent reports whether the agents, not the server, check repo, svc's
// upstream: it is on a private registry, or a public one refused it anonymously.
func CheckedByAgent(svc store.Service, repo string) bool {
	return repo != "" && !serverChecks(svc, repo)
}

// PrivateRepositories lists upstream repositories the server cannot check itself,
// for the agent to check. credentials_ref is the registry host, so an agent finds
// credentials for registry.example.com in GOLIASH_CREDENTIAL_REGISTRY_EXAMPLE_COM.
func (c *Checker) PrivateRepositories(ctx context.Context, sc store.Scope) ([]agentproto.RegistryCheck, error) {
	st, err := c.load(ctx, sc)
	if err != nil {
		return nil, err
	}
	pending, err := c.pendingLookups(ctx, sc)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []agentproto.RegistryCheck
	for id, ref := range st.refs {
		if ref.Repo == "" || serverChecks(st.byID[id], ref.Repo) || seen[ref.Repo] {
			continue
		}
		seen[ref.Repo] = true
		host, _, _ := strings.Cut(ref.Repo, "/")
		check := agentproto.RegistryCheck{Repository: ref.Repo, CredentialsRef: &host, Resolve: pending[ref.Repo]}
		if p, _, err := PolicyFor(st.byID[id], ref.Repo); err == nil && p.TagFilter != "" {
			filter := p.TagFilter
			check.TagFilter = &filter
		}
		out = append(out, check)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repository < out[j].Repository })
	return out, nil
}

// RecordPrivateTags records tags an agent found in a private repository for every
// service whose upstream it is.
func (c *Checker) RecordPrivateTags(ctx context.Context, sc store.Scope, repo string, tags []string, checkErr string) error {
	st, err := c.load(ctx, sc)
	if err != nil {
		return err
	}
	if checkErr == "" {
		c.queueMoving(ctx, sc, st, repo, tags)
	}
	for id, ref := range st.refs {
		if ref.Repo != repo {
			continue
		}
		if checkErr != "" {
			_ = c.store.RecordUpstreamCheck(ctx, sc, id, "agent: "+checkErr)
			continue
		}
		evs, err := c.recordTags(ctx, sc, st.byID[id], ref.Repo, tags, ref.Tag)
		if err != nil {
			return err
		}
		c.emit(sc, evs)
	}
	return nil
}
