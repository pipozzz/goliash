// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

// codeOwnersFiles are where GitHub, GitLab and Gitea look for CODEOWNERS, in the order they look.
var codeOwnersFiles = []string{".github/CODEOWNERS", ".gitlab/CODEOWNERS", ".gitea/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

// codeOwnersTTL is how long a repository's CODEOWNERS is kept before it is read again.
const codeOwnersTTL = time.Hour

// CodeOwners tells the team a repository's CODEOWNERS gives a path, reading each repository at most once an hour.
type CodeOwners struct {
	read  func(ctx context.Context, repo, branch, file string) ([]byte, error)
	now   func() time.Time
	mu    sync.Mutex
	cache map[string]cachedOwners
}

type cachedOwners struct {
	rules []ownerRule
	err   error
	at    time.Time
}

// NewCodeOwners reads CODEOWNERS with the router's providers.
func NewCodeOwners(r Router) *CodeOwners {
	return &CodeOwners{read: r.ReadFile, now: time.Now, cache: map[string]cachedOwners{}}
}

// Team is the team CODEOWNERS on the repository's branch gives the path (a directory or a file): the first team
// (@org/team) of the last line that covers it, "" when none does or the repository has no CODEOWNERS.
func (c *CodeOwners) Team(ctx context.Context, repo, branch, file string) (string, error) {
	key := repo + "@" + branch
	c.mu.Lock()
	got, ok := c.cache[key]
	c.mu.Unlock()
	if !ok || c.now().Sub(got.at) > codeOwnersTTL {
		got = cachedOwners{at: c.now()}
		for _, f := range codeOwnersFiles {
			raw, err := c.read(ctx, repo, branch, f)
			if err != nil {
				if ctx.Err() != nil {
					return "", err
				}
				got.err = err
				break
			}
			if raw != nil {
				got.rules = parseCodeOwners(raw)
				break
			}
		}
		c.mu.Lock()
		c.cache[key] = got
		c.mu.Unlock()
	}
	if got.err != nil {
		return "", got.err
	}
	return teamFor(got.rules, file), nil
}

// ownerRule is one line of CODEOWNERS: a pattern and the owners it names.
type ownerRule struct {
	match  *regexp.Regexp
	owners []string
}

// parseCodeOwners reads CODEOWNERS lines, skipping comments, GitLab's [Section] headers and patterns it cannot read.
func parseCodeOwners(raw []byte) []ownerRule {
	var rules []ownerRule
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") || strings.HasPrefix(fields[0], "[") ||
			strings.HasPrefix(fields[0], "^[") || strings.HasPrefix(fields[0], "!") {
			continue
		}
		re, err := regexp.Compile(patternRegexp(fields[0]))
		if err != nil {
			continue
		}
		rules = append(rules, ownerRule{re, fields[1:]})
	}
	return rules
}

// patternRegexp turns a CODEOWNERS pattern (gitignore's rules) into a regular expression that matches a path the
// pattern covers: the path itself, or one under a directory it names.
func patternRegexp(p string) string {
	anchored := strings.HasPrefix(p, "/") || strings.Contains(strings.TrimSuffix(p, "/"), "/")
	p = strings.Trim(p, "/")
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			b.WriteString("(.*/)?")
			i += 2
		case strings.HasPrefix(p[i:], "/**"):
			b.WriteString("(/.*)?")
			i += 2
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i++
		case p[i] == '*':
			b.WriteString("[^/]*")
		case p[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(p[i : i+1]))
		}
	}
	if anchored {
		return "^" + b.String() + "(/.*)?$"
	}
	return "^(.*/)?" + b.String() + "(/.*)?$"
}

// teamFor is the team of the last rule covering the path: its first owner written @org/team, as team.
func teamFor(rules []ownerRule, file string) string {
	file = strings.Trim(file, "/")
	if file == "" || file == "." {
		file = "_" // the whole repository: only lines for every file (*, **) cover it
	}
	for i := len(rules) - 1; i >= 0; i-- {
		if !rules[i].match.MatchString(file) {
			continue
		}
		for _, o := range rules[i].owners {
			if strings.HasPrefix(o, "@") && strings.Contains(o, "/") {
				return o[strings.LastIndex(o, "/")+1:]
			}
		}
		return "" // owned by people or by nobody: no team to name
	}
	return ""
}
