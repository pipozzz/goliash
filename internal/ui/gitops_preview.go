// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/gitops"
	"github.com/pipozzz/goliash/internal/store"
)

// demoPulls says whether pull requests are the demo's: previewed by anyone, opened by no one.
func (s *Server) demoPulls() bool {
	_, ok := s.pulls.(gitops.Demo)
	return ok
}

// GitOpsPreviewView is a pull request before Goliash opens it: where, the change to each file, and its text.
type GitOpsPreviewView struct {
	Base
	Service, ServiceURL string
	Repo, RepoURL       string
	From, Head          string // the branch it starts from, and the one it adds
	Title, Body         string
	Files               []PreviewFile
	Problem             string // why it cannot be opened
	CanOpen             bool
	OpenURL             string
	TargetID            string
	WorkloadID          string
	To                  string
	Return              string
	OpenAlready         string // the pull request opened before for it
}

// PreviewFile is one changed file, as the lines around its changes.
type PreviewFile struct {
	Path  string
	Lines []DiffLine
}

// DiffLine is one line of a change: kind "del", "add", " " (context) or "…" (lines left out).
type DiffLine struct {
	Kind, Text string
}

// previewGitOpsPR shows the pull request an upgrade would open, without opening it.
func (s *Server) previewGitOpsPR(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := r.PathValue("name")
	path := "/services/" + url.PathEscape(name)
	if s.pulls == nil || !p.Can(store.RoleMember) && !s.demoPulls() {
		http.NotFound(w, r)
		return nil
	}
	q := r.URL.Query()
	u, ok, err := s.findUpgrade(ctx, p.Scope, name, q.Get("target_id"), q.Get("workload_id"), q.Get("to"))
	if err != nil {
		return err
	}
	if !ok {
		return back(w, r, path, "error", "That upgrade is no longer offered; the page is reloaded with what runs now.")
	}
	ret := q.Get("return")
	if ret != "/delivery" && ret != "/updates" && ret != "/security" && ret != "/gitops" {
		ret = ""
	}
	if ret != "" {
		u.page = ret
	}
	by := p.User.Email
	if s.demoPulls() {
		by = "you"
	}
	s.addSummary(ctx, p.Scope, &u)
	req := s.bumpRequest(u, by)
	v := GitOpsPreviewView{
		Base: s.base(ctx, p, "matrix", "Pull request preview"), Service: name, ServiceURL: path,
		Repo: strings.TrimPrefix(req.Repo, "https://"), RepoURL: req.Repo, Head: req.Head, Title: req.Title, Body: req.Body,
		TargetID: u.app.TargetID, WorkloadID: u.app.WorkloadID, To: u.to, Return: ret,
		OpenURL: path + "/gitops/pr", CanOpen: p.Can(store.RoleMember) && !s.demoPulls(),
	}
	v.OpenAlready, _ = s.openPull(ctx, p.Scope, u.app, u.to)
	plan, err := s.pulls.Plan(ctx, req)
	if err != nil {
		v.Problem, v.CanOpen = bumpError(err, req), false
	}
	v.From = plan.Base
	for _, c := range plan.Changes {
		v.Files = append(v.Files, PreviewFile{Path: c.Path, Lines: diffLines(string(c.Old), string(c.New), 2)})
	}
	return render(w, r, GitOpsPreviewPage(v))
}

// diffLines is the change between two versions of a file with the same lines but some changed, as Goliash makes
// them, with context lines around each change.
func diffLines(before, after string, context int) []DiffLine {
	a, b := strings.Split(before, "\n"), strings.Split(after, "\n")
	if len(a) != len(b) { // not a change Goliash makes: show the new file whole
		out := make([]DiffLine, 0, len(b))
		for _, l := range b {
			out = append(out, DiffLine{"add", l})
		}
		return out
	}
	show := make([]bool, len(a))
	for i := range a {
		if a[i] != b[i] {
			for j := max(0, i-context); j <= min(len(a)-1, i+context); j++ {
				show[j] = true
			}
		}
	}
	var out []DiffLine
	gap := false
	for i := range a {
		if !show[i] {
			gap = true
			continue
		}
		if gap {
			out = append(out, DiffLine{"…", ""})
		}
		gap = false
		if a[i] == b[i] {
			out = append(out, DiffLine{" ", a[i]})
			continue
		}
		out = append(out, DiffLine{"del", a[i]}, DiffLine{"add", b[i]})
	}
	return out
}

func diffClass(kind string) string {
	switch kind {
	case "add":
		return "diff-add"
	case "del":
		return "diff-del"
	case "…":
		return "diff-gap"
	}
	return ""
}

func diffMark(kind string) string {
	switch kind {
	case "add":
		return "+ "
	case "del":
		return "- "
	case "…":
		return "…"
	}
	return "  "
}

// previewQuery is the query of a preview link.
func previewQuery(targetID, workloadID, to, ret string) string {
	q := url.Values{"target_id": {targetID}, "workload_id": {workloadID}, "to": {to}}
	if ret != "" {
		q.Set("return", ret)
	}
	return q.Encode()
}

var (
	prBold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	prCode = regexp.MustCompile("`([^`]+)`")
	prLink = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^\s)]+)\)`)
)

// markdownHTML renders the little markdown Goliash writes in pull requests (paragraphs, "- " lists, "### "
// headings, bold, code, links) as HTML. Everything is escaped first, so only these few tags come out.
func markdownHTML(md string) string {
	var b strings.Builder
	inList := false
	inline := func(s string) string {
		s = html.EscapeString(s)
		s = prCode.ReplaceAllString(s, "<code>$1</code>")
		s = prBold.ReplaceAllString(s, "<strong>$1</strong>")
		return prLink.ReplaceAllString(s, `<a href="$2" target="_blank" rel="noopener">$1</a>`)
	}
	for _, line := range strings.Split(md, "\n") {
		item, isItem := strings.CutPrefix(line, "- ")
		if inList && !isItem {
			b.WriteString("</ul>")
			inList = false
		}
		switch {
		case isItem:
			if !inList {
				b.WriteString("<ul>")
				inList = true
			}
			b.WriteString("<li>" + inline(item) + "</li>")
		case strings.HasPrefix(line, "### "):
			b.WriteString("<h3>" + inline(strings.TrimPrefix(line, "### ")) + "</h3>")
		case strings.TrimSpace(line) != "":
			b.WriteString("<p>" + inline(line) + "</p>")
		}
	}
	if inList {
		b.WriteString("</ul>")
	}
	return b.String()
}
