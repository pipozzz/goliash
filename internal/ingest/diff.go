// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/mapping"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// diffInput is everything needed to turn one snapshot into changes.
type diffInput struct {
	Target   store.Target
	Snapshot agentproto.Snapshot
	Existing []store.Instance // every instance ever seen on the target
	// LastVersions is, by instance, the version the history last said it changed to: where a rollout that ran
	// two versions side by side came from.
	LastVersions map[string]store.VersionMark
	Mapper       *mapping.Mapper
	EnvByName    map[string]string // environment name -> ID
	// ServiceID resolves a service name from a label, creating the service if needed.
	ServiceID func(name string) (string, error)
	// AppLabel is the workspace's own label key for applications, tried first.
	AppLabel string
	// Baseline is true for a target's first processed snapshot: instances are recorded
	// but no events are emitted, so onboarding does not flood the history.
	Baseline bool
	// AutoMap maps the main container of an unmapped workload to the service suggested for its image;
	// AutoMapped collects image repository -> service ID for the rules that keep it so.
	AutoMap    bool
	AutoMapped map[string]string
}

// slot is one container of one workload; its version is what events track.
type slot struct{ workload, container string }

// diff computes the instance upserts, removals and events for a snapshot.
func diff(in diffInput) (store.SnapshotChanges, error) {
	ch := store.SnapshotChanges{
		Scope: in.Target.Scope, SnapshotID: in.Snapshot.SnapshotID, TargetID: in.Target.ID, At: in.Snapshot.CollectedAt,
	}
	existing := map[string]store.Instance{}
	for _, i := range in.Existing {
		existing[i.Key()] = i
	}

	observed := map[string]bool{}
	observedWorkloads := map[string]bool{}
	for _, w := range in.Snapshot.Workloads {
		observedWorkloads[w.ID] = true
		ns := ""
		if w.Namespace != nil {
			ns = *w.Namespace
		}
		app, appSource := workloadApp(w.Labels, in.AppLabel, ns, string(w.Kind), w.Name)
		mw := mapping.Workload{Name: w.Name, Labels: w.Labels, App: app}

		type row struct {
			inst     store.Instance
			decision mapping.Decision
		}
		var rows []row
		names := map[string]bool{}
		for _, c := range w.Containers {
			ref := versions.ParseImage(c.Image)
			d := in.Mapper.Map(mw, c.Name, ref)
			if d.Ignore {
				continue
			}
			digest := ref.Digest
			if c.Digest != nil {
				digest = *c.Digest
			}
			inst := store.Instance{
				Scope: in.Target.Scope, TargetID: in.Target.ID, EnvironmentID: in.Target.EnvironmentID,
				WorkloadID: w.ID, WorkloadKind: string(w.Kind), WorkloadName: w.Name,
				ContainerName: c.Name, Image: c.Image, Tag: ref.Tag, Digest: digest, Running: c.Running, Desired: w.DesiredReplicas,
				SuggestedService: d.Suggested, ServiceID: d.ServiceID,
			}
			if w.Namespace != nil {
				inst.Namespace = *w.Namespace
			}
			inst.App, inst.AppSource = app, appSource
			inst.Team = workloadTeam(w.Labels)
			inst.Exposed = exposed(w)
			inst.Maintenance = w.Maintenance
			if w.CodeDigest != nil {
				inst.CodeDigest = *w.CodeDigest
			}
			if d.EnvName != "" {
				if id, ok := in.EnvByName[d.EnvName]; ok {
					inst.EnvironmentID = id
				}
			}
			if d.ServiceName != "" {
				id, err := in.ServiceID(d.ServiceName)
				if err != nil {
					return ch, err
				}
				inst.ServiceID = id
			}
			rows = append(rows, row{inst, d})
			names[c.Name] = true
		}

		containerNames := make([]string, 0, len(names))
		for n := range names {
			containerNames = append(containerNames, n)
		}
		serviceName := ""
		if len(rows) > 0 && rows[0].decision.ServiceName != "" {
			serviceName = rows[0].decision.ServiceName
		}
		main := mapping.MainContainer(mw, containerNames, serviceName)

		for _, r := range rows {
			r.inst.IsMain = r.inst.ContainerName == main
			if in.AutoMap && r.inst.IsMain && r.inst.ServiceID == "" && validServiceName(r.inst.SuggestedService) {
				id, err := in.ServiceID(r.inst.SuggestedService)
				if err != nil {
					return ch, err
				}
				r.inst.ServiceID = id
				// No image rule for a shared image (a Lambda runtime, a database engine): it would take every function or
				// database; they map by name each time instead.
				if repo := versions.ParseImage(r.inst.Image).Repo(); repo != "" && in.AutoMapped != nil && !mapping.SharedImage(repo) {
					in.AutoMapped[repo] = id
				}
			}
			if prev, ok := existing[r.inst.Key()]; ok {
				r.inst.ID = prev.ID
			} else {
				r.inst.ID = store.NewID()
			}
			observed[r.inst.Key()] = true
			ch.Upsert = append(ch.Upsert, r.inst)
		}
	}

	// Instances no longer observed are removed, unless the snapshot is incomplete and
	// their workload is missing from it (it may just not have been readable).
	for _, i := range in.Existing {
		if !i.Active() || observed[i.Key()] {
			continue
		}
		if in.Snapshot.Complete || observedWorkloads[i.WorkloadID] {
			ch.Remove = append(ch.Remove, i.ID)
		}
	}

	if !in.Baseline {
		ch.Events = events(in.Existing, ch.Upsert, ch.Remove, in.LastVersions, in.Snapshot.CollectedAt)
	}
	return ch, nil
}

// events compares, per main container slot, the dominant version before and after.
func events(existing, upsert []store.Instance, removed []string, last map[string]store.VersionMark, at time.Time) []store.Event {
	removedSet := map[string]bool{}
	for _, id := range removed {
		removedSet[id] = true
	}
	before := map[slot][]store.Instance{}
	isNew := map[string]bool{}
	for _, i := range existing {
		if i.Active() && i.IsMain {
			before[slot{i.WorkloadID, i.ContainerName}] = append(before[slot{i.WorkloadID, i.ContainerName}], i)
		}
	}
	activeBefore := map[string]bool{}
	for _, i := range existing {
		if i.Active() {
			activeBefore[i.ID] = true
		}
	}
	after := map[slot][]store.Instance{}
	for _, i := range upsert {
		if i.IsMain {
			after[slot{i.WorkloadID, i.ContainerName}] = append(after[slot{i.WorkloadID, i.ContainerName}], i)
		}
		isNew[i.ID] = !activeBefore[i.ID]
	}
	// Slots that stopped being main (a sidecar picked differently) keep their rows in
	// "after" only if still present; treat them as unchanged rather than removed.
	for s := range before {
		if _, ok := after[s]; ok {
			continue
		}
		stillPresent := false
		for _, i := range upsert {
			if i.WorkloadID == s.workload && i.ContainerName == s.container {
				stillPresent = true
				break
			}
		}
		if stillPresent {
			delete(before, s)
		}
	}

	slots := map[slot]bool{}
	for s := range before {
		slots[s] = true
	}
	for s := range after {
		slots[s] = true
	}
	ordered := make([]slot, 0, len(slots))
	for s := range slots {
		ordered = append(ordered, s)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].workload != ordered[j].workload {
			return ordered[i].workload < ordered[j].workload
		}
		return ordered[i].container < ordered[j].container
	})

	// When each instance of a slot was first seen: rows of the snapshot carry no date, new ones arrive now.
	firstSeen := map[string]time.Time{}
	for _, i := range existing {
		firstSeen[i.ID] = i.FirstSeenAt
	}
	for _, i := range upsert {
		if _, ok := firstSeen[i.ID]; !ok {
			firstSeen[i.ID] = at
		}
	}
	var out []store.Event
	for _, s := range ordered {
		old, cur := dominant(before[s], nil), dominant(after[s], isNew)
		if old != nil && cur != nil {
			// While two versions run side by side (a rolling update, a canary), which one has more replicas
			// flips as containers start and restart. A change forward is said when the new version takes over; a
			// change back only once the older version runs alone, so a restarting new container is not a rollback.
			if mixed(before[s]) {
				from := settled(existing, s, last)
				if from == "" { // no history yet: the version that ran there first
					from = earliest(before[s]).Tag
				}
				o := store.Instance{Tag: from} // no digest: a change of version, not a retag
				for _, i := range before[s] {
					if i.Tag == from {
						o = i
					}
				}
				old = &o
			}
			if mixed(after[s]) && old.Tag != cur.Tag {
				for _, i := range after[s] {
					if i.Tag == old.Tag && !firstSeen[cur.ID].After(firstSeen[i.ID]) {
						cur = nil // the older version is winning again, but the newer still runs: wait
						break
					}
				}
				if cur == nil {
					continue
				}
			}
		}
		switch {
		case old == nil && cur != nil:
			out = append(out, event("deployed", *cur, "", cur.Tag, "", at))
		case old != nil && cur == nil:
			if allRemoved(before[s], removedSet) {
				out = append(out, event("removed", *old, old.Tag, "", "", at))
			}
		case old != nil && cur != nil:
			switch {
			case old.Tag != cur.Tag:
				out = append(out, event("version_changed", *cur, old.Tag, cur.Tag, "", at))
			case old.Digest != "" && cur.Digest != "" && old.Digest != cur.Digest:
				out = append(out, event("version_changed", *cur, old.Tag, cur.Tag, "retag", at))
			}
		}
	}
	return out
}

// settled is the version the history last said a slot changed to, "" when it says nothing.
func settled(existing []store.Instance, s slot, last map[string]store.VersionMark) string {
	var latest store.VersionMark
	for _, i := range existing {
		if i.WorkloadID != s.workload || i.ContainerName != s.container {
			continue
		}
		if m, ok := last[i.ID]; ok && (latest.Version == "" || m.At.After(latest.At)) {
			latest = m
		}
	}
	return latest.Version
}

// earliest is the instance first seen.
func earliest(rows []store.Instance) store.Instance {
	best := rows[0]
	for _, r := range rows[1:] {
		if r.FirstSeenAt.Before(best.FirstSeenAt) {
			best = r
		}
	}
	return best
}

// mixed reports whether more than one version is present in a slot, running or (re)starting.
func mixed(rows []store.Instance) bool {
	for _, r := range rows {
		if r.Tag != rows[0].Tag {
			return true
		}
	}
	return false
}

// dominant is the instance with the most running replicas; ties go to the newer one,
// then to the higher tag.
func dominant(rows []store.Instance, isNew map[string]bool) *store.Instance {
	var best *store.Instance
	for i := range rows {
		r := &rows[i]
		switch {
		case best == nil, r.Running > best.Running:
			best = r
		case r.Running == best.Running && isNew[r.ID] && !isNew[best.ID]:
			best = r
		case r.Running == best.Running && isNew[r.ID] == isNew[best.ID] && strings.Compare(r.Tag, best.Tag) > 0:
			best = r
		}
	}
	return best
}

func allRemoved(rows []store.Instance, removed map[string]bool) bool {
	for _, r := range rows {
		if !removed[r.ID] {
			return false
		}
	}
	return true
}

func event(typ string, i store.Instance, from, to, note string, at time.Time) store.Event {
	return store.Event{
		Type: typ, ServiceID: i.ServiceID, EnvironmentID: i.EnvironmentID, TargetID: i.TargetID, InstanceID: i.ID,
		FromVersion: from, ToVersion: to, Note: note, Source: "poll", At: at,
	}
}

// serviceNamePattern is what the inbox accepts as a service name.
var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

func validServiceName(name string) bool { return serviceNamePattern.MatchString(name) }

// exposed is how a workload can be reached from outside, as its agent read it; a goliash.exposed label says so
// instead: internet (whatever the agent saw) or internal (nothing).
func exposed(w agentproto.Workload) []string {
	switch strings.ToLower(w.Labels["goliash.exposed"]) {
	case "internal", "false", "no":
		return nil
	case "internet", "true", "yes":
		return []string{"label goliash.exposed"}
	}
	return w.Exposed
}
