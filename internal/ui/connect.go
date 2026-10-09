// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Connect walks someone through watching a new cluster or host in one form: the
// environment, who collects it (a new agent, an agent they have, or the server), and
// the platform's settings as fields rather than JSON. The result page shows how to
// start the agent and follows the first report live.

// PlatformInfo describes a platform on the connect pages.
type PlatformInfo struct {
	Key        string
	Title      string
	Summary    string
	ServerHint string // when collecting from the server makes sense; empty when it rarely does
	AgentFirst bool   // a new agent is the usual way
}

var connectPlatforms = []PlatformInfo{
	{
		Key: "kubernetes", Title: "Kubernetes", Summary: "Deployments, StatefulSets, DaemonSets, CronJobs and Jobs in a cluster.", AgentFirst: true,
		ServerHint: "The server can watch the cluster it runs in when installed with collectInCluster=true.",
	},
	{Key: "docker", Title: "Docker host", Summary: "Containers on one Docker engine, optionally only some Compose projects.", AgentFirst: true},
	{Key: "host", Title: "Linux server", Summary: "The operating system and installed packages (dpkg, apk) of a server or VM, with their end of life.", AgentFirst: true},
	{Key: "swarm", Title: "Docker Swarm", Summary: "Services of a Swarm, read on a manager node.", AgentFirst: true},
	{
		Key: "nomad", Title: "Nomad", Summary: "Jobs of a Nomad region, with an ACL token that may list and read jobs.", AgentFirst: true,
		ServerHint: "The server can call the Nomad API itself when it reaches the address.",
	},
	{Key: "ecs", Title: "Amazon ECS", Summary: "Services of ECS clusters in a region, and private ECR tags.", AgentFirst: true},
	{
		Key: "lambda", Title: "AWS Lambda", Summary: "Functions of a region: their container images, or the runtime of .zip functions and its end of life.",
		AgentFirst: true, ServerHint: "The server can read Lambda itself when it runs with AWS credentials that may list functions.",
	},
	{
		Key: "compose", Title: "Compose files", Summary: "What Compose files in Git declare, without a Docker engine.",
		ServerHint: "The server fetches the files from their URLs; no agent needed.",
	},
}

func platformInfo(key string) (PlatformInfo, bool) {
	for _, p := range connectPlatforms {
		if p.Key == key {
			return p, true
		}
	}
	return PlatformInfo{}, false
}

// ConnectView is the platform picker.
type ConnectView struct {
	Base
	Platforms []PlatformInfo
}

// ConnectFormView is the form for one platform.
type ConnectFormView struct {
	Base
	Platform PlatformInfo
	Envs     []EnvView
	Agents   []AgentOption
	Form     url.Values // values to show again after a mistake
}

// ConnectDoneView is what to do next, and the live status of the new target.
type ConnectDoneView struct {
	Base
	Platform  PlatformInfo
	TargetID  string
	Target    string
	Env       string
	AgentName string
	AgentID   string
	NewToken  string // set when an agent was created
	ByServer  bool
	ServerURL string
	Version   string   // the server's release, which the snippets install
	Settings  string   // pretty JSON, for the record
	Files     []string // compose files, for the agent's mounts
	Status    ConnectStatus
}

// ConnectStatus is how far a new target got.
type ConnectStatus struct {
	TargetID  string
	Step      int // 0 waiting for the agent, 1 agent connected, 2 first report in
	Agent     string
	Problem   string
	Workloads int
	Mapped    int
	Unmapped  int
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	return render(w, r, ConnectPage(ConnectView{Base: withFlash(s.base(r.Context(), p, "agents", "Connect"), r), Platforms: connectPlatforms}))
}

func (s *Server) connectForm(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	return s.showConnectForm(w, r, p, r.URL.Query(), "")
}

func (s *Server) showConnectForm(w http.ResponseWriter, r *http.Request, p auth.Principal, form url.Values, problem string) error {
	info, ok := platformInfo(r.PathValue("platform"))
	if !ok {
		return back(w, r, "/connect", "error", "Pick what to connect.")
	}
	all, err := s.agentsView(r.Context(), p)
	if err != nil {
		return err
	}
	v := ConnectFormView{
		Base: withFlash(s.base(r.Context(), p, "agents", "Connect "+info.Title), r), Platform: info,
		Envs: all.Envs, Agents: all.Moves, Form: form,
	}
	if problem != "" {
		v.Error = problem
	}
	return render(w, r, ConnectFormPage(v))
}

// fieldList splits a comma, space or newline separated field.
func fieldList(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' })
}

// connectSettings builds a target's stored settings from the form. problem explains a
// missing or wrong value.
func connectSettings(platform string, f url.Values) (json.RawMessage, string) {
	val := func(k string) string { return strings.TrimSpace(f.Get(k)) }
	var t agentproto.Target
	switch platform {
	case "kubernetes":
		k := agentproto.KubernetesSettings{IncludeNamespaces: fieldList(val("include")), ExcludeNamespaces: fieldList(val("exclude"))}
		if c := val("context"); c != "" {
			k.KubeconfigContext = &c
		}
		t.Kubernetes = &k
	case "docker":
		host := val("docker_host")
		if host == "" {
			return nil, "Enter the Docker API address, e.g. tcp://socket-proxy:2375."
		}
		t.Docker = &agentproto.DockerSettings{DockerHost: host, Projects: fieldList(val("projects"))}
	case "host":
		root := val("root")
		if root == "" {
			root = "/host"
		}
		h := agentproto.HostSettings{Root: &root, Packages: fieldList(val("packages"))}
		if f.Get("all_packages") == "on" {
			all := true
			h.AllPackages = &all
		}
		t.Host = &h
	case "swarm":
		host := val("docker_host")
		if host == "" {
			return nil, "Enter the Docker API address of a manager, e.g. tcp://socket-proxy:2375."
		}
		t.Swarm = &agentproto.SwarmSettings{DockerHost: host}
	case "nomad":
		addr := val("address")
		if u, err := url.Parse(addr); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, "Enter the Nomad address, e.g. http://nomad.service.consul:4646."
		}
		n := agentproto.NomadSettings{Address: addr, Namespaces: fieldList(val("namespaces"))}
		if region := val("region"); region != "" {
			n.Region = &region
		}
		t.Nomad = &n
		if f.Get("acl") == "on" {
			ref := "nomad"
			t.CredentialsRef = &ref
		}
	case "ecs":
		region := val("region")
		if region == "" {
			return nil, "Enter the AWS region, e.g. eu-west-1."
		}
		t.Ecs = &agentproto.ECSSettings{Region: region, Clusters: fieldList(val("clusters"))}
	case "lambda":
		region := val("region")
		if region == "" {
			return nil, "Enter the AWS region, e.g. eu-west-1."
		}
		t.Lambda = &agentproto.LambdaSettings{Region: region, NamePrefixes: fieldList(val("prefixes"))}
		for _, pair := range fieldList(val("aliases")) {
			alias, env, ok := strings.Cut(pair, "=")
			if !ok || alias == "" || env == "" {
				return nil, "Map aliases as alias=environment, e.g. live=prod, canary=-."
			}
			if t.Lambda.AliasEnvironments == nil {
				t.Lambda.AliasEnvironments = map[string]string{}
			}
			t.Lambda.AliasEnvironments[alias] = env
		}
	case "compose":
		files := fieldList(val("files"))
		if len(files) == 0 {
			return nil, "Enter at least one Compose file: a URL, or a path on the agent."
		}
		c := agentproto.ComposeSettings{Files: files}
		if project := val("project"); project != "" {
			c.Project = &project
		}
		t.Compose = &c
		if f.Get("private") == "on" {
			ref := "compose"
			t.CredentialsRef = &ref
		}
	default:
		return nil, "Unknown platform."
	}
	b, err := json.Marshal(t)
	if err != nil {
		return nil, err.Error()
	}
	return b, ""
}

func (s *Server) connectCreate(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	info, ok := platformInfo(r.PathValue("platform"))
	if !ok {
		return back(w, r, "/connect", "error", "Pick what to connect.")
	}
	if err := r.ParseForm(); err != nil {
		return err
	}
	f := r.PostForm
	again := func(problem string) error { return s.showConnectForm(w, r, p, f, problem) }
	if f.Get("mode") == "code" && info.AgentFirst {
		return s.connectCode(w, r, p, info, f)
	}

	name := strings.TrimSpace(f.Get("name"))
	if !serviceName.MatchString(name) {
		return again("Name the target with letters, digits, dots, dashes and underscores, e.g. prod-eu.")
	}
	settings, problem := connectSettings(info.Key, f)
	if problem != "" {
		return again(problem)
	}

	env, problem, err := s.connectEnv(r, p, f)
	if err != nil {
		return err
	}
	if problem != "" {
		return again(problem)
	}

	// Who collects it.
	v := ConnectDoneView{Platform: info, Target: name, Env: env.Name, ServerURL: s.publicURL, Version: s.version}
	t := store.Target{Scope: p.Scope, EnvironmentID: env.ID, Platform: info.Key, Name: name, Settings: settings}
	switch by := f.Get("by"); by {
	case "server":
		v.ByServer = true
	case "new", "":
		agentName := strings.TrimSpace(f.Get("agent_name"))
		if agentName == "" {
			agentName = name
		}
		if !serviceName.MatchString(agentName) {
			return again("Name the agent with letters, digits, dots, dashes and underscores.")
		}
		token, hash := tokens.New(tokens.Agent)
		a, err := s.store.CreateAgent(ctx, p.Scope, agentName, hash)
		if err != nil {
			return again("There is an agent " + agentName + " already; pick it, or choose another name.")
		}
		s.audit(ctx, p, "agent.create", "agent", agentName)
		t.AgentID, v.AgentID, v.AgentName, v.NewToken = a.ID, a.ID, a.Name, token
	default:
		a, err := s.store.GetAgent(ctx, p.Scope, by)
		if err != nil {
			return again("Pick an agent.")
		}
		t.AgentID, v.AgentID, v.AgentName = a.ID, a.ID, a.Name
	}
	created, err := s.store.CreateTarget(ctx, t)
	if err != nil {
		return again("There is a target " + name + " already; choose another name.")
	}
	s.audit(ctx, p, "target.create", "target", name, "platform", info.Key, "environment", env.Name, "agent", v.AgentName)

	v.Base = s.base(ctx, p, "agents", "Connect "+info.Title)
	v.TargetID = created.ID
	v.Files = fieldList(f.Get("files"))
	var pretty strings.Builder
	enc := json.NewEncoder(&pretty)
	enc.SetIndent("", "  ")
	_ = enc.Encode(settings)
	v.Settings = strings.TrimSpace(pretty.String())
	if v.Status, err = s.connectStatus(r, p, created.ID); err != nil {
		return err
	}
	if v.NewToken != "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	return render(w, r, ConnectDonePage(v))
}

// connectEnv is the environment the form picked: one that exists, or a new one
// placed after the others. problem explains a wrong choice.
func (s *Server) connectEnv(r *http.Request, p auth.Principal, f url.Values) (store.Environment, string, error) {
	ctx := r.Context()
	envName := strings.TrimSpace(f.Get("new_env"))
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return store.Environment{}, "", err
	}
	if envName == "" {
		for _, e := range envs {
			if e.ID == f.Get("env") {
				return e, "", nil
			}
		}
		return store.Environment{}, "Pick an environment, or name a new one.", nil
	}
	if !serviceName.MatchString(envName) {
		return store.Environment{}, "Name the environment with letters, digits, dots, dashes and underscores, e.g. staging.", nil
	}
	pos := 10
	for _, e := range envs {
		if strings.EqualFold(e.Name, envName) {
			return store.Environment{}, "There is an environment " + e.Name + " already; pick it from the list.", nil
		}
		pos = max(pos, e.Position+10)
	}
	env, err := s.store.CreateEnvironment(ctx, p.Scope, envName, pos)
	if err != nil {
		return env, "", err
	}
	s.audit(ctx, p, "environment.create", "environment", envName)
	return env, "", nil
}

// ConnectCodeView shows the command with a new enrollment code, and follows the
// agents that enroll with it.
type ConnectCodeView struct {
	Base
	Platform PlatformInfo
	Env      string
	Code     string
	Many     bool
	Expires  time.Time // zero: never
	Title    string
	Command  string
	Status   CodeStatus
}

// CodeStatus is what the agents that enrolled with a code got to.
type CodeStatus struct {
	CodeID   string
	Platform string // what the command started, for troubleshooting hints
	Many     bool
	Agents   []CodeAgent
	Done     bool // every agent's targets reported, for a code for one agent
	Slow     bool // no agent a minute after the code was made
}

// CodeAgent is one agent that enrolled with a code.
type CodeAgent struct {
	ID        string
	Name      string
	Connected bool
	Targets   []CodeTarget
	Notes     []string // what it could not use
}

// CodeTarget is a target an enrolled agent added.
type CodeTarget struct {
	ID        string
	Name      string
	Platform  string
	Reported  bool
	Workloads int
	Unmapped  int
	Problem   string
}

// Expiry choices for enrollment codes: how long new agents may enroll.
var codeExpiry = map[string]time.Duration{"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "never": 0}

// connectCode creates an enrollment code and shows the command that starts an agent
// with it: the agent registers itself and adds what it finds.
func (s *Server) connectCode(w http.ResponseWriter, r *http.Request, p auth.Principal, info PlatformInfo, f url.Values) error {
	ctx := r.Context()
	again := func(problem string) error { return s.showConnectForm(w, r, p, f, problem) }
	name := strings.TrimSpace(f.Get("cluster_name"))
	if name != "" && !serviceName.MatchString(name) {
		return again("Name the cluster with letters, digits, dots, dashes and underscores, e.g. prod-eu.")
	}
	validity, ok := codeExpiry[f.Get("expires")]
	if !ok {
		validity = codeExpiry["7d"]
	}
	env, problem, err := s.connectEnv(r, p, f)
	if err != nil {
		return err
	}
	if problem != "" {
		return again(problem)
	}
	var until time.Time
	if validity > 0 {
		until = time.Now().Add(validity)
	}
	many := f.Get("many") == "on"
	code, hash := tokens.New(tokens.Enroll)
	c, err := s.store.CreateEnrollmentCode(ctx, p.Scope, env.ID, hash, p.User.Email, until, !many)
	if err != nil {
		return err
	}
	s.audit(ctx, p, "enroll.create", "environment", env.Name, "platform", info.Key, "code", c.ID)
	v := ConnectCodeView{
		Base: s.base(ctx, p, "agents", "Connect "+info.Title), Platform: info, Env: env.Name, Code: code, Many: many,
		Expires: until,
	}
	v.Title, v.Command = installCommand(info.Key, s.publicURL, code, nil, s.version)
	if name != "" && info.Key == "kubernetes" {
		v.Command += " --set name=" + name
	}
	if v.Status, err = s.codeStatus(r, p, c.ID, info.Key); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return render(w, r, ConnectCodePage(v))
}

func (s *Server) codeStatus(r *http.Request, p auth.Principal, codeID, platform string) (CodeStatus, error) {
	ctx := r.Context()
	c, err := s.store.GetEnrollmentCode(ctx, p.Scope, codeID)
	if err != nil {
		return CodeStatus{}, err
	}
	st := CodeStatus{CodeID: c.ID, Many: !c.Single, Platform: platform}
	agents, err := s.store.ListCodeAgents(ctx, p.Scope, c.ID)
	if err != nil {
		return st, err
	}
	reported := len(agents) > 0
	for _, a := range agents {
		ca := CodeAgent{ID: a.ID, Name: a.Name, Connected: !a.LastSeenAt.IsZero(), Notes: a.Notes}
		ts, err := s.store.ListAgentTargets(ctx, p.Scope, a.ID)
		if err != nil {
			return st, err
		}
		for _, t := range ts {
			ct := CodeTarget{ID: t.ID, Name: t.Name, Platform: t.Platform, Reported: !t.LastSnapshotAt.IsZero(), Problem: t.CollectorError}
			if ct.Reported {
				one, err := s.connectStatus(r, p, t.ID)
				if err != nil {
					return st, err
				}
				ct.Workloads, ct.Unmapped = one.Workloads, one.Unmapped
			}
			reported = reported && ct.Reported
			ca.Targets = append(ca.Targets, ct)
		}
		reported = reported && len(ts) > 0
		st.Agents = append(st.Agents, ca)
	}
	st.Done = reported && !st.Many
	st.Slow = len(agents) == 0 && time.Since(c.CreatedAt) > time.Minute
	return st, nil
}

// connectCodeProgress is the live fragment of the code page.
func (s *Server) connectCodeProgress(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	platform := r.URL.Query().Get("p")
	if _, ok := platformInfo(platform); !ok {
		platform = ""
	}
	st, err := s.codeStatus(r, p, r.PathValue("id"), platform)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "unknown code", http.StatusNotFound)
		return nil
	}
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return render(w, r, codeStatusView(st))
}

func (s *Server) connectStatus(r *http.Request, p auth.Principal, targetID string) (ConnectStatus, error) {
	ctx := r.Context()
	t, err := s.store.GetTarget(ctx, p.Scope, targetID)
	if err != nil {
		return ConnectStatus{}, err
	}
	st := ConnectStatus{TargetID: t.ID, Problem: t.CollectorError}
	if t.AgentID == "" {
		st.Agent, st.Step = "the server", 1
	} else if a, err := s.store.GetAgent(ctx, p.Scope, t.AgentID); err == nil {
		st.Agent = "agent " + a.Name
		if !a.LastSeenAt.IsZero() {
			st.Step = 1
		}
	}
	if !t.LastSnapshotAt.IsZero() {
		st.Step = 2
		inst, err := s.store.ListTargetInstances(ctx, p.Scope, t.ID)
		if err != nil {
			return st, err
		}
		seen := map[string]bool{}
		for _, in := range inst {
			if !in.RemovedAt.IsZero() || !in.IsMain || seen[in.WorkloadID] {
				continue
			}
			seen[in.WorkloadID] = true
			st.Workloads++
			if in.ServiceID != "" {
				st.Mapped++
			} else {
				st.Unmapped++
			}
		}
	}
	return st, nil
}

// connectProgress is the live status fragment the result page polls.
func (s *Server) connectProgress(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	st, err := s.connectStatus(r, p, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "unknown target", http.StatusNotFound)
		return nil
	}
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return render(w, r, connectStatusView(st, time.Now()))
}

// agentRelease is the release snippets install: the agent and files matching this
// server's version, not whatever latest and main are by then. A development build
// uses latest and main.
func agentRelease(version string) (imageTag, gitRef string) {
	v := strings.TrimPrefix(version, "v")
	if v == "" || v == "dev" || !strings.Contains(v, ".") {
		return "latest", "main"
	}
	return v, "v" + v
}

// installCommand is how to start an agent for a platform, with the server URL and the
// token filled in and the release pinned to the server's, so it can be pasted as it is.
func installCommand(platform, serverURL, token string, files []string, version string) (title, cmd string) {
	tag, ref := agentRelease(version)
	raw := "https://raw.githubusercontent.com/pipozzz/goliash/" + ref + "/deploy/"
	image := "ghcr.io/pipozzz/goliash-agent:" + tag
	chartVersion := ""
	if tag != "latest" {
		chartVersion = " --version " + tag
	}
	switch platform {
	case "kubernetes":
		return "Install the agent in the cluster with Helm", "helm upgrade --install goliash-agent oci://ghcr.io/pipozzz/charts/goliash-agent" + chartVersion + " \\\n" +
			"  -n goliash --create-namespace --set serverURL=" + serverURL + " --set token.value=" + token
	case "docker":
		return "Start the agent on the Docker host", "curl -fsSL " + raw + "docker/goliash-agent.yml | \\\n" +
			"  GOLIASH_AGENT_VERSION=" + tag + " GOLIASH_SERVER_URL=" + serverURL + " GOLIASH_AGENT_TOKEN=" + token + " \\\n" +
			"  docker compose -p goliash-agent -f - up -d"
	case "host":
		return "Start the agent on the server, its root mounted read-only", "docker run -d --name goliash-agent --restart unless-stopped \\\n" +
			"  -v goliash-agent:/data -v /:/host:ro -e GOLIASH_HOST_ROOT=/host \\\n" +
			"  -e GOLIASH_SERVER_URL=" + serverURL + " -e GOLIASH_AGENT_TOKEN=" + token + " \\\n" +
			"  " + image + "\n" +
			"# Without Docker: run the goliash-agent binary on the server with GOLIASH_HOST_ROOT=/ (root set to / in the target)"
	case "swarm":
		return "Deploy the agent stack on a manager node", "curl -fsSLO " + raw + "swarm/goliash-agent.yml\n" +
			"printf '%s' '" + token + "' | docker secret create goliash_agent_token -\n" +
			"GOLIASH_AGENT_VERSION=" + tag + " GOLIASH_SERVER_URL=" + serverURL + " docker stack deploy -c goliash-agent.yml goliash-agent"
	case "nomad":
		return "Run the agent job in Nomad", "curl -fsSLO " + raw + "nomad/goliash-agent.nomad.hcl\n" +
			"nomad var put -force nomad/jobs/goliash-agent token=" + token + "\n" +
			"# with Nomad ACLs, add nomad_token=<secret ID> above (a token that may list-jobs and read-job)\n" +
			"nomad job run -var server_url=" + serverURL + " -var version=" + tag + " goliash-agent.nomad.hcl"
	case "ecs", "lambda":
		lambda := "false"
		title := "Run the agent on ECS Fargate with CloudFormation (default VPC)"
		if platform == "lambda" {
			lambda, title = "true", "Run the agent on ECS Fargate with CloudFormation; it watches the region's functions"
		}
		return title, "curl -fsSLo goliash-agent.cfn.yaml " + raw + "ecs/goliash-agent.cfn.yaml && \\\n" +
			"aws cloudformation deploy --stack-name goliash-agent --template-file goliash-agent.cfn.yaml --capabilities CAPABILITY_IAM \\\n" +
			"  --parameter-overrides ServerURL=" + serverURL + " EnrollCode=" + token + " Version=" + tag + " WatchLambda=" + lambda + " \\\n" +
			"  Subnets=$(aws ec2 describe-subnets --filters Name=default-for-az,Values=true --query 'Subnets[].SubnetId' --output text | tr '\\t' ,)\n" +
			"# Own subnets: Subnets=subnet-a,subnet-b AssignPublicIp=DISABLED. Terraform: https://goliash.dev/install/ecs/"
	}
	// Compose files on the agent's disk: mount their directories at the same paths, so
	// the paths in the target's settings are right inside the container too.
	var mounts, dirs []string
	seen := map[string]bool{}
	for _, f := range files {
		if strings.Contains(f, "://") {
			continue
		}
		d := path.Dir(f)
		if !seen[d] {
			seen[d] = true
			mounts = append(mounts, "-v "+d+":"+d+":ro")
			dirs = append(dirs, d)
		}
	}
	extra := ""
	if len(dirs) > 0 {
		extra = "  " + strings.Join(mounts, " ") + " -e GOLIASH_COMPOSE_DIRS=" + strings.Join(dirs, ",") + " \\\n"
	}
	return "Start the agent where the files are", "docker run -d --name goliash-agent --restart unless-stopped \\\n" +
		"  -v goliash-agent:/data \\\n" + extra +
		"  -e GOLIASH_SERVER_URL=" + serverURL + " -e GOLIASH_AGENT_TOKEN=" + token + " \\\n" +
		"  " + image
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// reportSummary says what the first report found, and what to do about it.
func reportSummary(st ConnectStatus) string {
	switch {
	case st.Workloads == 0:
		return "Nothing runs there yet, or the settings point elsewhere: check them with Edit below."
	case st.Unmapped == 0:
		return "All of them belong to a service already: they are in the matrix."
	case st.Mapped == 0:
		return plural(st.Unmapped, "workload waits", "workloads wait") + " in the inbox: tell Goliash which service each is, once per image."
	}
	return plural(st.Mapped, "belongs", "belong") + " to a service; " + plural(st.Unmapped, "waits", "wait") + " in the inbox."
}

// setupPage creates the first account with the link from the server log.
func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if !s.auth.SetupTokenValid(r.Context(), token) {
		http.Redirect(w, r, "/login?error=setup", http.StatusSeeOther)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	_ = render(w, r, SetupPage(token, r.URL.Query().Get("error")))
}

// agentLogs is where to read a new agent's log on each platform.
func agentLogs(platform string) string {
	switch platform {
	case "kubernetes":
		return "kubectl -n goliash logs deploy/goliash-agent"
	case "docker":
		return "docker compose -p goliash-agent logs agent"
	case "swarm":
		return "docker service logs goliash-agent_agent"
	case "nomad":
		return "nomad alloc logs -job goliash-agent"
	case "ecs", "lambda":
		return "aws logs tail /ecs/goliash-agent --follow"
	}
	return "docker logs goliash-agent"
}

// upgradeCommand updates an agent that was started with the Connect command on
// platform to version, keeping its code or token and settings. Empty for platforms
// where it was started by hand.
func upgradeCommand(platform, serverURL, version string) (title, cmd string) {
	tag, ref := agentRelease(version)
	raw := "https://raw.githubusercontent.com/pipozzz/goliash/" + ref + "/deploy/"
	switch platform {
	case "kubernetes":
		chart := ""
		if tag != "latest" {
			chart = " --version " + tag
		}
		return "Update with Helm, keeping its values", "helm upgrade goliash-agent oci://ghcr.io/pipozzz/charts/goliash-agent" + chart +
			" -n goliash --reuse-values"
	case "docker":
		return "Update on the Docker host, keeping its code", "curl -fsSL " + raw + "docker/goliash-agent.yml | \\\n" +
			"  GOLIASH_AGENT_VERSION=" + tag + " GOLIASH_SERVER_URL=" + serverURL + " \\\n" +
			"  GOLIASH_AGENT_TOKEN=$(docker inspect goliash-agent-agent-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^GOLIASH_AGENT_TOKEN=//p') \\\n" +
			"  docker compose -p goliash-agent -f - up -d"
	case "swarm":
		return "Update the service on a manager node", "docker service update --image ghcr.io/pipozzz/goliash-agent:" + tag + " goliash-agent_agent"
	case "nomad":
		return "Run the job again with the new version", "curl -fsSLO " + raw + "nomad/goliash-agent.nomad.hcl\n" +
			"nomad job run -var server_url=" + serverURL + " -var version=" + tag + " goliash-agent.nomad.hcl"
	case "ecs", "lambda":
		keep := ""
		for _, p := range []string{"ServerURL", "EnrollCode", "Subnets", "AssignPublicIp", "Cluster", "SecurityGroups", "WatchLambda", "Regions", "Clusters", "CpuArchitecture"} {
			keep += " ParameterKey=" + p + ",UsePreviousValue=true"
		}
		return "Update the CloudFormation stack (with Terraform: set image to the new tag)",
			"aws cloudformation update-stack --stack-name goliash-agent --use-previous-template --capabilities CAPABILITY_IAM \\\n" +
				"  --parameters ParameterKey=Version,ParameterValue=" + tag + keep
	}
	return "", ""
}
