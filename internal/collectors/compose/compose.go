// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package compose reads Compose files as declared, without a Docker engine: every
// service with an image is a workload running its declared replicas. Files come from
// HTTP(S) URLs, such as a raw file in a Git forge, or, in the agent, from directories
// the agent's operator allowed.
package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// DirsEnv lists the directories (separated by commas) the agent may read Compose files
// from. Without it, only URLs are read.
const DirsEnv = "GOLIASH_COMPOSE_DIRS"

// maxFile bounds a Compose file.
const maxFile = 1 << 20

// New is the collectors.Factory for compose targets in the agent: URLs, and files in
// the directories listed in GOLIASH_COMPOSE_DIRS.
func New(_ context.Context, t agentproto.Target) (collectors.Collector, error) {
	return newCollector(t, allowedDirs(os.Getenv(DirsEnv)))
}

// NewRemote is the factory the server uses for targets it collects itself: URLs only,
// so a target cannot read the server's own files.
func NewRemote(_ context.Context, t agentproto.Target) (collectors.Collector, error) {
	return newCollector(t, nil)
}

func newCollector(t agentproto.Target, dirs []string) (*Collector, error) {
	if t.Compose == nil || len(t.Compose.Files) == 0 {
		return nil, errors.New("compose target has no files")
	}
	c := &Collector{settings: *t.Compose, dirs: dirs, http: safeClient()}
	for _, f := range t.Compose.Files {
		if isURL(f) {
			continue
		}
		if len(dirs) == 0 {
			return nil, fmt.Errorf("%s: only URLs are allowed here; an agent reads files from the directories in %s", f, DirsEnv)
		}
		if !c.allowed(f) {
			return nil, fmt.Errorf("%s is outside the directories in %s", f, DirsEnv)
		}
	}
	if t.CredentialsRef != nil && *t.CredentialsRef != "" {
		token, err := collectors.Credential(*t.CredentialsRef)
		if err != nil {
			return nil, err
		}
		c.token = token
	}
	return c, nil
}

// Collector reads one set of Compose files.
type Collector struct {
	settings agentproto.ComposeSettings
	dirs     []string
	token    string
	http     *http.Client
}

func allowedDirs(env string) []string {
	var out []string
	for _, d := range strings.Split(env, ",") {
		if d = strings.TrimSpace(d); d != "" {
			if abs, err := filepath.Abs(d); err == nil {
				out = append(out, filepath.Clean(abs))
			}
		}
	}
	return out
}

// allowed reports whether path lies in one of the allowed directories, after
// resolving symbolic links.
func (c *Collector) allowed(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	for _, d := range c.dirs {
		if real, err := filepath.EvalSymlinks(d); err == nil {
			d = real
		}
		if rel, err := filepath.Rel(d, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

// safeClient refuses link-local addresses such as cloud metadata endpoints.
func safeClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(host); ip == nil || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("address %s is not allowed", host)
		}
		return nil
	}}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialer.DialContext
	tr.Proxy = nil
	return &http.Client{Timeout: 30 * time.Second, Transport: tr}
}

// Collect reads and merges the files and reports their services.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	merged := map[string]*service{}
	var order []string
	project := ""
	if c.settings.Project != nil {
		project = *c.settings.Project
	}
	for _, f := range c.settings.Files {
		raw, err := c.read(ctx, f)
		if err != nil {
			return collectors.Result{}, err
		}
		file, err := parse(raw, c.settings.Variables)
		if err != nil {
			return collectors.Result{}, fmt.Errorf("%s: %w", display(f), err)
		}
		if project == "" {
			project = file.Name
		}
		for name, s := range file.Services {
			if merged[name] == nil {
				merged[name] = &service{}
				order = append(order, name)
			}
			merged[name].override(s)
		}
	}
	if project == "" {
		project = "compose"
	}

	sort.Strings(order)
	res := collectors.Result{Complete: true, Workloads: []agentproto.Workload{}}
	for _, name := range order {
		s := merged[name]
		if s.Image == "" {
			continue // built from source; nothing to compare with a registry
		}
		replicas := s.replicas()
		ns := project
		image, digest := collectors.SplitImage(s.Image)
		ct := agentproto.Container{Name: name, Image: image, Running: replicas}
		if digest != "" {
			ct.Digest = &digest
		}
		res.Workloads = append(res.Workloads, agentproto.Workload{
			ID: project + "/" + name, Kind: agentproto.ComposeService, Namespace: &ns, Name: name,
			Labels: s.labels, DesiredReplicas: &replicas, Containers: []agentproto.Container{ct},
		})
	}
	return res, nil
}

// display hides URL credentials and query strings in errors.
func display(f string) string {
	if u, err := url.Parse(f); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host + u.Path
	}
	return f
}

func (c *Collector) read(ctx context.Context, f string) ([]byte, error) {
	if !isURL(f) {
		if !c.allowed(f) {
			return nil, fmt.Errorf("%s is outside the directories in %s", f, DirsEnv)
		}
		file, err := os.Open(f) //nolint:gosec // checked against the allowed directories
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close() }()
		return readLimited(file, f)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", display(f), err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", display(f), errors.Unwrap(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", display(f), resp.StatusCode)
	}
	return readLimited(resp.Body, display(f))
}

func readLimited(r io.Reader, name string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, maxFile)
	}
	return b, nil
}

type file struct {
	Name     string                 `yaml:"name"`
	Services map[string]serviceYAML `yaml:"services"`
}

type serviceYAML struct {
	Image  string    `yaml:"image"`
	Scale  *int      `yaml:"scale"`
	Labels yaml.Node `yaml:"labels"`
	Deploy struct {
		Replicas *int `yaml:"replicas"`
	} `yaml:"deploy"`
}

// parse reads a Compose document and substitutes variables in its values, as Compose
// does after parsing. Its errors never quote the file, which may not be a Compose file.
func parse(raw []byte, vars map[string]string) (file, error) {
	var f file
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return f, errors.New("not valid YAML")
	}
	interpolateNode(&doc, vars)
	if err := doc.Decode(&f); err != nil {
		return f, errors.New("not a Compose file")
	}
	if f.Services == nil {
		return f, errors.New("no services")
	}
	return f, nil
}

// service is a service after merging override files.
type service struct {
	Image  string
	scale  *int
	deploy *int
	labels map[string]string
}

func (s *service) override(y serviceYAML) {
	if y.Image != "" {
		s.Image = y.Image
	}
	if y.Scale != nil {
		s.scale = y.Scale
	}
	if y.Deploy.Replicas != nil {
		s.deploy = y.Deploy.Replicas
	}
	if l := labels(y.Labels); l != nil {
		if s.labels == nil {
			s.labels = map[string]string{}
		}
		for k, v := range l {
			s.labels[k] = v
		}
	}
}

func (s *service) replicas() int {
	switch {
	case s.scale != nil && *s.scale >= 0:
		return *s.scale
	case s.deploy != nil && *s.deploy >= 0:
		return *s.deploy
	}
	return 1
}

// labels reads both forms: a mapping, or a list of "key=value".
func labels(n yaml.Node) map[string]string {
	out := map[string]string{}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			out[n.Content[i].Value] = n.Content[i+1].Value
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			k, v, _ := strings.Cut(item.Value, "=")
			out[k] = v
		}
	default:
		return nil
	}
	return out
}

func interpolateNode(n *yaml.Node, vars map[string]string) {
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "$") {
		n.Value = interpolate(n.Value, vars)
		if n.Style == 0 {
			n.Tag = "" // resolve again: "${REPLICAS:-2}" becomes the number 2
		}
	}
	for _, c := range n.Content {
		interpolateNode(c, vars)
	}
}

var variable = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?[-?+])([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// interpolate substitutes ${VAR}, ${VAR:-default}, ${VAR-default}, ${VAR:+alt},
// ${VAR?err} and $VAR like Compose; $$ is a literal $. Unset variables are empty.
func interpolate(text string, vars map[string]string) string {
	return variable.ReplaceAllStringFunc(text, func(m string) string {
		if m == "$$" {
			return "$"
		}
		g := variable.FindStringSubmatch(m)
		name, op, arg := g[1], g[2], g[3]
		if name == "" {
			name = g[4]
		}
		val, set := vars[name]
		switch op {
		case ":-":
			if val == "" {
				return arg
			}
		case "-":
			if !set {
				return arg
			}
		case ":+":
			if val != "" {
				return arg
			}
			return ""
		case "+":
			if set {
				return arg
			}
			return ""
		}
		return val
	})
}
