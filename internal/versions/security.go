// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// securityIDs finds the vulnerability identifiers release notes name.
var securityIDs = regexp.MustCompile(`\b(CVE-\d{4}-\d{4,}|GHSA(?:-[0-9a-z]{4}){3}|GO-\d{4}-\d{3,})\b`)

// securityWords finds release notes that say they fix security problems without naming them.
var securityWords = regexp.MustCompile(`(?i)\bsecurity (fix|fixes|release|issue|issues|vulnerabilit(y|ies)|advisor(y|ies)|update)\b|\bfix(es|ed)? (a |an |the )?(critical |high )?vulnerabilit`)

// SecurityIn says what security fixes release notes name: their CVE, GHSA and GO identifiers, comma separated, or
// "security" when they say so without one; "" when they say nothing of it.
func SecurityIn(notes string) string {
	ids := securityIDs.FindAllString(notes, -1)
	if len(ids) == 0 {
		if securityWords.MatchString(notes) || securityLines.MatchString(notes) {
			return "security"
		}
		return ""
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		id = strings.ToUpper(id)
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// phpNetURL is the PHP project's releases feed: every release of a major, with a "security" tag on those that fix
// vulnerabilities. Its {major} is replaced.
var phpNetURL = "https://www.php.net/releases/index.php?json&version={major}&max=200"

// phpFeed reads php.net's releases of a major once an hour: version → it is a security release.
type phpFeed struct {
	mu    sync.Mutex
	http  *http.Client
	cache map[string]phpFeedEntry
}

type phpFeedEntry struct {
	at       time.Time
	security map[string]bool
}

func newPHPFeed() *phpFeed {
	return &phpFeed{http: &http.Client{Timeout: 20 * time.Second}, cache: map[string]phpFeedEntry{}}
}

// securityReleases are the versions of one PHP major the PHP project tags as security releases.
func (f *phpFeed) securityReleases(ctx context.Context, major int) (map[string]bool, error) {
	key := fmt.Sprint(major)
	f.mu.Lock()
	e, ok := f.cache[key]
	f.mu.Unlock()
	if ok && time.Since(e.at) < time.Hour {
		return e.security, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.ReplaceAll(phpNetURL, "{major}", key), nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("php.net: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("php.net answered %d", resp.StatusCode)
	}
	var feed map[string]struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&feed); err != nil {
		return nil, fmt.Errorf("php.net: %w", err)
	}
	out := map[string]bool{}
	for v, r := range feed {
		for _, t := range r.Tags {
			if t == "security" {
				out[v] = true
			}
		}
	}
	f.mu.Lock()
	f.cache[key] = phpFeedEntry{at: time.Now(), security: out}
	f.mu.Unlock()
	return out, nil
}

// securityRecent is how long after Goliash first saw a release a security fix learned of it is still news.
const securityRecent = 3 * 24 * time.Hour

// securityRelease is the announcement that releases fixing vulnerabilities came out for a service running an
// older version: the newest of them, newly flagged, that is new (added in this check, or seen in the last days).
func securityRelease(svc store.Service, running string, flagged []store.Release, added []string, now time.Time) (store.Event, bool) {
	cur, ok := ParseVersion(running)
	if !ok {
		return store.Event{}, false
	}
	isNew := map[string]bool{}
	for _, a := range added {
		isNew[a] = true
	}
	var best *store.Release
	var bestV Version
	for i := range flagged {
		r := &flagged[i]
		v, ok := ParseVersion(r.Version)
		if !ok || v.Compare(cur) <= 0 {
			continue
		}
		if !isNew[r.Version] && now.Sub(r.DiscoveredAt) > securityRecent {
			continue
		}
		if best == nil || v.Compare(bestV) > 0 {
			best, bestV = r, v
		}
	}
	if best == nil {
		return store.Event{}, false
	}
	return store.Event{
		Type: "security_release", ServiceID: svc.ID, FromVersion: running, ToVersion: best.Version,
		Note: best.Security, Source: "poll", At: now,
	}, true
}

// securityLines finds changelog lines that mark a change as a security fix ("*) Security: …" in nginx's).
var securityLines = regexp.MustCompile(`(?m)^\s*(\*\)\s*)?Security:`)

// goReleasesURL is Go's release history: "go1.25.13 (released …) includes security fixes to …".
var goReleasesURL = "https://go.dev/doc/devel/release"

// goReleaseLine finds one release and what it includes in the history, its markup stripped.
var goReleaseLine = regexp.MustCompile(`go(\d+\.\d+(?:\.\d+)?) \(released [0-9-]+\) includes (.*?)\.`)

// textFeeds reads what projects publish as pages: Go's release history, and per-version release notes pages
// (PostgreSQL's), kept a day (a page that failed, an hour).
type textFeeds struct {
	mu    sync.Mutex
	http  *http.Client
	pages map[string]textPage
}

type textPage struct {
	at   time.Time
	text string
	ok   bool
}

func newTextFeeds() *textFeeds {
	return &textFeeds{http: &http.Client{Timeout: 20 * time.Second}, pages: map[string]textPage{}}
}

// text is a page's text, its markup stripped and its spaces folded.
func (f *textFeeds) text(ctx context.Context, url string) (string, bool) {
	f.mu.Lock()
	p, ok := f.pages[url]
	f.mu.Unlock()
	if ok && (time.Since(p.at) < time.Hour || p.ok && time.Since(p.at) < 24*time.Hour) {
		return p.text, p.ok
	}
	p = textPage{at: time.Now()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err == nil {
		var resp *http.Response
		if resp, err = f.http.Do(req); err == nil {
			if resp.StatusCode == http.StatusOK {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
				p.text, p.ok = pageText(string(b)), true
			}
			_ = resp.Body.Close()
		}
	}
	f.mu.Lock()
	f.pages[url] = p
	f.mu.Unlock()
	return p.text, p.ok
}

var (
	htmlTags   = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`)
	htmlSpaces = regexp.MustCompile(`\s+`)
)

// pageText is an HTML page's text.
func pageText(page string) string {
	t := htmlTags.ReplaceAllString(page, " ")
	t = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&#39;", "'", "&quot;", `"`).Replace(t)
	return htmlSpaces.ReplaceAllString(t, " ")
}

// goSecurity are the Go releases whose history entry includes security fixes: version (1.25.13) → true.
func (f *textFeeds) goSecurity(ctx context.Context) map[string]bool {
	text, ok := f.text(ctx, goReleasesURL)
	if !ok {
		return nil
	}
	out := map[string]bool{}
	for _, m := range goReleaseLine.FindAllStringSubmatch(text, -1) {
		if strings.Contains(m[2], "security fix") {
			out[m[1]] = true
		}
	}
	return out
}

// changelogPage is a release's own notes page from a changelog template, or "" when the template has a #fragment:
// it points into a page about many releases (PHP's ChangeLog, Python's cumulative changelog), whose text names
// other releases' fixes too.
func changelogPage(template string, v Version) string {
	if !strings.Contains(template, "{version}") || strings.Contains(template, "#") {
		return ""
	}
	return strings.ReplaceAll(template, "{version}", versionNumber(v))
}

// cycleOf is the release cycle a version belongs to: the cycle whose name its version starts with ("3.12" for
// 3.12.7), the longest such.
func cycleOf(cycles []EOLCycle, v Version) (EOLCycle, bool) {
	num := versionNumber(v)
	var best EOLCycle
	found := false
	for _, cy := range cycles {
		if (num == cy.Name || strings.HasPrefix(num, cy.Name+".")) && len(cy.Name) > len(best.Name) {
			best, found = cy, true
		}
	}
	return best, found
}
