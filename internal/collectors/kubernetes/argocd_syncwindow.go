// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// argoProjects are Argo CD's AppProjects, read for their sync windows. Without the permission (get and list on
// appprojects.argoproj.io) no application is ever reported as blocked by one.
var argoProjects = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}

// syncWindow is one of an AppProject's spec.syncWindows: while an allow window is open, or no deny window is,
// Argo CD syncs the applications it matches.
type syncWindow struct {
	deny                               bool
	schedule                           string
	cron                               *cronSpec
	duration                           time.Duration
	loc                                *time.Location
	applications, namespaces, clusters []string
	and                                bool // andOperator: every selector given must match, not any
}

// argoSyncWindows reads every AppProject's sync windows, by project name.
func (c *Collector) argoSyncWindows(ctx context.Context) map[string][]syncWindow {
	list, err := c.dyn.Resource(argoProjects).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	out := map[string][]syncWindow{}
	for _, p := range list.Items {
		ws, _, _ := unstructured.NestedSlice(p.Object, "spec", "syncWindows")
		for _, w := range ws {
			m, ok := w.(map[string]any)
			if !ok {
				continue
			}
			if sw, ok := parseSyncWindow(m); ok {
				out[p.GetName()] = append(out[p.GetName()], sw)
			}
		}
	}
	return out
}

func parseSyncWindow(m map[string]any) (syncWindow, bool) {
	w := syncWindow{deny: str(m["kind"]) == "deny", schedule: str(m["schedule"]), loc: time.UTC}
	if !w.deny && str(m["kind"]) != "allow" {
		return w, false
	}
	var err error
	if w.cron, err = parseCron(w.schedule); err != nil {
		return w, false
	}
	if w.duration, err = time.ParseDuration(str(m["duration"])); err != nil || w.duration <= 0 {
		return w, false
	}
	if tz := str(m["timeZone"]); tz != "" {
		if w.loc, err = time.LoadLocation(tz); err != nil {
			return w, false
		}
	}
	list := func(k string) []string {
		var out []string
		if vs, ok := m[k].([]any); ok {
			for _, v := range vs {
				if s := str(v); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	}
	w.applications, w.namespaces, w.clusters = list("applications"), list("namespaces"), list("clusters")
	w.and, _ = m["andOperator"].(bool)
	return w, true
}

// matches tells whether the window applies to the application, by Argo CD's rules: its name, destination
// namespace or destination cluster (server or name) matches one of the window's globs.
func (w syncWindow) matches(app, namespace string, clusters ...string) bool {
	glob := func(patterns []string, values ...string) bool {
		for _, p := range patterns {
			for _, v := range values {
				if ok, _ := path.Match(p, v); ok && v != "" {
					return true
				}
			}
		}
		return false
	}
	sel := []struct {
		patterns []string
		values   []string
	}{{w.applications, []string{app}}, {w.namespaces, []string{namespace}}, {w.clusters, clusters}}
	some, all := false, true
	for _, s := range sel {
		if len(s.patterns) == 0 {
			continue
		}
		if glob(s.patterns, s.values...) {
			some = true
		} else {
			all = false
		}
	}
	if w.and {
		return some && all
	}
	return some
}

// maxWindow bounds how far the windows are searched, in either direction.
const maxWindow = 8 * 24 * time.Hour

// openUntil tells whether the window is open at now, and until when: the latest start within its duration
// before now, plus the duration.
func (w syncWindow) openUntil(now time.Time) (time.Time, bool) {
	now = now.In(w.loc).Truncate(time.Minute)
	d := min(w.duration, maxWindow)
	for t := now; now.Sub(t) < d; t = t.Add(-time.Minute) {
		if w.cron.at(t) {
			return t.Add(w.duration), true
		}
	}
	return time.Time{}, false
}

// nextOpen is when the window opens next after now, within maxWindow.
func (w syncWindow) nextOpen(now time.Time) (time.Time, bool) {
	now = now.In(w.loc).Truncate(time.Minute)
	for t := now.Add(time.Minute); t.Sub(now) <= maxWindow; t = t.Add(time.Minute) {
		if w.cron.at(t) {
			return t, true
		}
	}
	return time.Time{}, false
}

// syncBlocked tells whether Argo CD's sync windows keep the application from syncing at now, why, and until
// when (zero when that is not known). As in Argo CD, an open deny window blocks, and so does an application
// having allow windows none of which is open.
func syncBlocked(windows []syncWindow, now time.Time, app, namespace string, clusters ...string) (why string, until time.Time) {
	var allows []syncWindow
	allowOpen := false
	for _, w := range windows {
		if !w.matches(app, namespace, clusters...) {
			continue
		}
		end, open := w.openUntil(now)
		if w.deny {
			if open && end.After(until) {
				why, until = fmt.Sprintf("deny window %q for %s", w.schedule, shortDuration(w.duration)), end
			}
			continue
		}
		allows = append(allows, w)
		allowOpen = allowOpen || open
	}
	if why != "" {
		return why, until.UTC()
	}
	if len(allows) == 0 || allowOpen {
		return "", time.Time{}
	}
	why = fmt.Sprintf("outside the allow window %q", allows[0].schedule)
	if len(allows) > 1 {
		why = "outside its allow windows"
	}
	for _, w := range allows {
		if next, ok := w.nextOpen(now); ok && (until.IsZero() || next.Before(until)) {
			until = next
		}
	}
	return why, until.UTC()
}

// shortDuration writes 48h0m0s as 48h and 1h30m0s as 1h30m.
func shortDuration(d time.Duration) string {
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	return strings.TrimSuffix(s, "0m")
}

// cronSpec is a standard five-field cron schedule (minute hour day-of-month month day-of-week), as Argo CD's
// sync windows use: lists, ranges, steps, month and weekday names, and the @daily-style descriptors.
type cronSpec struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

var cronDescriptors = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *", "@weekly": "0 0 * * 0",
	"@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var (
	cronMonths = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	cronDays   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
)

func parseCron(s string) (*cronSpec, error) {
	s = strings.TrimSpace(s)
	if d, ok := cronDescriptors[strings.ToLower(s)]; ok {
		s = d
	}
	f := strings.Fields(s)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron %q: want 5 fields", s)
	}
	c := &cronSpec{}
	var err error
	if c.minute, _, err = cronField(f[0], 0, 59, nil); err != nil {
		return nil, err
	}
	if c.hour, _, err = cronField(f[1], 0, 23, nil); err != nil {
		return nil, err
	}
	if c.dom, c.domStar, err = cronField(f[2], 1, 31, nil); err != nil {
		return nil, err
	}
	if c.month, _, err = cronField(f[3], 1, 12, cronMonths); err != nil {
		return nil, err
	}
	if c.dow, c.dowStar, err = cronField(f[4], 0, 7, cronDays); err != nil {
		return nil, err
	}
	if c.dow&(1<<7) != 0 { // 7 is Sunday too
		c.dow |= 1
	}
	return c, nil
}

// cronField parses one field into a bit set; star tells whether it was * (or ?), every value.
func cronField(s string, lo, hi int, names map[string]int) (bits uint64, star bool, err error) {
	num := func(v string) (int, error) {
		if n, ok := names[strings.ToLower(v)]; ok {
			return n, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return 0, fmt.Errorf("cron value %q out of %d-%d", v, lo, hi)
		}
		return n, nil
	}
	for _, part := range strings.Split(s, ",") {
		rng, step := part, 1
		if i := strings.IndexByte(part, '/'); i >= 0 {
			rng = part[:i]
			if step, err = strconv.Atoi(part[i+1:]); err != nil || step < 1 {
				return 0, false, fmt.Errorf("cron step %q", part)
			}
		}
		from, to := lo, hi
		switch {
		case rng == "*" || rng == "?":
			star = star || step == 1
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			if from, err = num(a); err != nil {
				return 0, false, err
			}
			if to, err = num(b); err != nil {
				return 0, false, err
			}
			if from > to {
				return 0, false, fmt.Errorf("cron range %q", rng)
			}
		default:
			if from, err = num(rng); err != nil {
				return 0, false, err
			}
			if step == 1 {
				to = from
			}
		}
		for v := from; v <= to; v += step {
			bits |= 1 << v
		}
	}
	return bits, star, nil
}

// at tells whether the schedule fires at t's minute. As in cron, when both day fields are restricted a day
// matching either one counts.
func (c *cronSpec) at(t time.Time) bool {
	if c.minute&(1<<t.Minute()) == 0 || c.hour&(1<<t.Hour()) == 0 || c.month&(1<<int(t.Month())) == 0 {
		return false
	}
	dom, dow := c.dom&(1<<t.Day()) != 0, c.dow&(1<<int(t.Weekday())) != 0
	if c.domStar || c.dowStar {
		return dom && dow
	}
	return dom || dow
}
