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
	{Key: "swarm", Title: "Docker Swarm", Summary: "Services of a Swarm, read on a manager node.", AgentFirst: true},
	{
		Key: "nomad", Title: "Nomad", Summary: "Jobs of a Nomad region, with an ACL token that may list and read jobs.", AgentFirst: true,
		ServerHint: "The server can call the Nomad API itself when it reaches the address.",
	},
	{Key: "ecs", Title: "Amazon ECS", Summary: "Services of ECS clusters in a region, and private ECR tags.", AgentFirst: true},
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

	name := strings.TrimSpace(f.Get("name"))
	if !serviceName.MatchString(name) {
		return again("Name the target with letters, digits, dots, dashes and underscores, e.g. prod-eu.")
	}
	settings, problem := connectSettings(info.Key, f)
	if problem != "" {
		return again(problem)
	}

	// The environment: one that exists, or a new one placed after the others.
	envName := strings.TrimSpace(f.Get("new_env"))
	var env store.Environment
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return err
	}
	if envName != "" {
		if !serviceName.MatchString(envName) {
			return again("Name the environment with letters, digits, dots, dashes and underscores, e.g. staging.")
		}
		pos := 10
		for _, e := range envs {
			if strings.EqualFold(e.Name, envName) {
				return again("There is an environment " + e.Name + " already; pick it from the list.")
			}
			pos = max(pos, e.Position+10)
		}
		if env, err = s.store.CreateEnvironment(ctx, p.Scope, envName, pos); err != nil {
			return err
		}
		s.audit(ctx, p, "environment.create", "environment", envName)
	} else {
		for _, e := range envs {
			if e.ID == f.Get("env") {
				env = e
			}
		}
		if env.ID == "" {
			return again("Pick an environment, or name a new one.")
		}
	}

	// Who collects it.
	v := ConnectDoneView{Platform: info, Target: name, Env: env.Name, ServerURL: s.publicURL}
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

const rawBase = "https://raw.githubusercontent.com/pipozzz/goliash/main/deploy/"

// installCommand is how to start an agent for a platform, with the server URL and the
// token filled in, so it can be pasted as it is.
func installCommand(platform, serverURL, token string, files []string) (title, cmd string) {
	switch platform {
	case "kubernetes":
		return "Install the agent in the cluster with Helm", "kubectl create namespace goliash --dry-run=client -o yaml | kubectl apply -f -\n" +
			"kubectl -n goliash create secret generic goliash-agent-token --from-literal=token=" + token + " \\\n" +
			"  --dry-run=client -o yaml | kubectl apply -f -\n" +
			"helm upgrade --install goliash-agent oci://ghcr.io/pipozzz/charts/goliash-agent -n goliash \\\n" +
			"  --set serverURL=" + serverURL + " --set token.existingSecret=goliash-agent-token"
	case "docker":
		return "Start the agent on the Docker host", "curl -fsSLO " + rawBase + "docker/goliash-agent.yml\n" +
			"GOLIASH_SERVER_URL=" + serverURL + " GOLIASH_AGENT_TOKEN=" + token + " \\\n" +
			"  docker compose -f goliash-agent.yml up -d"
	case "swarm":
		return "Deploy the agent stack on a manager node", "curl -fsSLO " + rawBase + "swarm/goliash-agent.yml\n" +
			"printf '%s' '" + token + "' | docker secret create goliash_agent_token -\n" +
			"GOLIASH_SERVER_URL=" + serverURL + " docker stack deploy -c goliash-agent.yml goliash-agent"
	case "nomad":
		return "Run the agent job in Nomad", "curl -fsSLO " + rawBase + "nomad/goliash-agent.nomad.hcl\n" +
			"sed -i.bak 's#https://goliash.example.com#" + serverURL + "#' goliash-agent.nomad.hcl\n" +
			"nomad var put -force nomad/jobs/goliash-agent token=" + token + " nomad_token=<Nomad ACL token>\n" +
			"nomad job run goliash-agent.nomad.hcl"
	case "ecs":
		return "Run the agent on ECS with Terraform", "aws secretsmanager create-secret --name goliash-agent-token --secret-string " + token + "\n\n" +
			"module \"goliash_agent\" {\n" +
			"  source             = \"github.com/pipozzz/goliash//deploy/ecs/goliash-agent\"\n" +
			"  cluster_arn        = aws_ecs_cluster.tools.arn\n" +
			"  server_url         = \"" + serverURL + "\"\n" +
			"  token_secret_arn   = \"<ARN printed above>\"\n" +
			"  subnet_ids         = module.vpc.private_subnets\n" +
			"  security_group_ids = [aws_security_group.egress_only.id]\n}"
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
		"  ghcr.io/pipozzz/goliash-agent:latest"
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
