// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package discover finds what an enrolling agent can collect where it runs, and what
// identifies that installation, so the agent needs no targets set up by hand and gets
// its own agent back when it enrolls again.
package discover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/pipozzz/goliash/internal/collectors/docker"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Result is what was found.
type Result struct {
	// Identity names the installation: the first platform found decides it. Empty
	// when nothing was found; codes for one agent do without it.
	Identity string
	// Name is the agent's preferred name.
	Name    string
	Targets []agentproto.DeclaredTarget
	// Notes explain platforms that looked present but could not be used.
	Notes []string
}

// Env reads the environment; os.Getenv in production.
type Env func(string) string

// Options let tests replace the system.
type Options struct {
	Env      Env
	ReadFile func(string) ([]byte, error)
	Exists   func(string) bool
	HTTP     *http.Client
	Hostname func() (string, error)
	// AWS reads what the agent's AWS credentials reach (default: the AWS SDK with the
	// default credential chain).
	AWS AWS
}

// AWS is what discovery asks of AWS: who the credentials are (which needs no
// permission), the ECS clusters of a region and whether Lambda functions may be listed.
type AWS interface {
	Account(ctx context.Context, region string) (string, error)
	ECSClusters(ctx context.Context, region string) ([]string, error) // ARNs
	CanListFunctions(ctx context.Context, region string) error
}

// Paths the agent looks at.
const (
	kubernetesCA = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	dockerSocket = "/var/run/docker.sock"
)

// Run looks for every platform, then adds the targets declared in GOLIASH_TARGETS
// (a JSON array of targets). GOLIASH_AGENT_ID overrides the identity and
// GOLIASH_AGENT_NAME the name.
func Run(ctx context.Context, o Options) (Result, error) {
	if o.Env == nil {
		o.Env = os.Getenv
	}
	if o.ReadFile == nil {
		o.ReadFile = os.ReadFile
	}
	if o.Exists == nil {
		o.Exists = func(p string) bool { _, err := os.Stat(p); return err == nil }
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if o.Hostname == nil {
		o.Hostname = os.Hostname
	}
	if o.AWS == nil {
		o.AWS = sdkAWS{}
	}
	var r Result
	found := func(identity, name string, t agentproto.DeclaredTarget) {
		if r.Identity == "" {
			r.Identity, r.Name = identity, name
		}
		r.Targets = append(r.Targets, t)
	}
	agentName := o.Env("GOLIASH_AGENT_NAME")
	nameOr := func(fallback string) string {
		if agentName != "" {
			return agentName
		}
		return fallback
	}

	// Kubernetes: in-cluster service account. The cluster CA tells clusters apart.
	if o.Env("KUBERNETES_SERVICE_HOST") != "" {
		if ca, err := o.ReadFile(kubernetesCA); err == nil {
			name := nameOr("kubernetes")
			found("kubernetes:"+shortHash(ca), name, agentproto.DeclaredTarget{
				Platform: agentproto.Kubernetes, Name: name, Kubernetes: &agentproto.KubernetesSettings{},
			})
		} else {
			r.Notes = append(r.Notes, "kubernetes: no service account CA: "+err.Error())
		}
	}

	// AWS: on ECS the task metadata names the agent's cluster and region; elsewhere
	// AWS_REGION does. The agent then adds what its credentials may read.
	awsRegion := o.Env("AWS_REGION")
	if awsRegion == "" {
		awsRegion = o.Env("AWS_DEFAULT_REGION")
	}
	ownCluster := ""
	if uri := o.Env("ECS_CONTAINER_METADATA_URI_V4"); uri != "" {
		if arn, err := ecsCluster(ctx, o.HTTP, uri); err == nil {
			ownCluster = arn
			awsRegion, _ = arnParts(arn)
		} else {
			r.Notes = append(r.Notes, "ecs: "+err.Error())
		}
	}
	if awsRegion != "" {
		o.discoverAWS(ctx, &r, found, nameOr, awsRegion, ownCluster)
	}

	// Nomad: NOMAD_ADDR, as the job sets it. Inside Nomad the job is what stays the
	// same when the agent moves to another node.
	if addr := o.Env("NOMAD_ADDR"); addr != "" {
		region := o.Env("NOMAD_REGION")
		if region == "" {
			region = "global"
		}
		where := addr
		if job := o.Env("NOMAD_JOB_ID"); job != "" {
			where = o.Env("NOMAD_NAMESPACE") + "/" + job
		}
		name := nameOr("nomad-" + region)
		t := agentproto.DeclaredTarget{Platform: agentproto.Nomad, Name: name, Nomad: &agentproto.NomadSettings{Address: addr}}
		if o.Env("NOMAD_REGION") != "" {
			t.Nomad.Region = &region
		}
		if o.Env("GOLIASH_CREDENTIAL_NOMAD") != "" {
			ref := "nomad"
			t.CredentialsRef = &ref
		}
		found("nomad:"+region+"@"+where, name, t)
	}

	// Docker: DOCKER_HOST (normally a socket proxy) or the socket. A Swarm manager is
	// collected as the swarm, any other engine as a Docker host.
	host := o.Env("DOCKER_HOST")
	if host == "" && o.Exists(dockerSocket) {
		host = "unix://" + dockerSocket
	}
	if host != "" {
		if info, err := dockerInfo(ctx, host); err == nil {
			hostName := info.Name
			if hostName == "" {
				hostName, _ = o.Hostname()
			}
			name := nameOr(hostName)
			if info.Swarm.LocalNodeState == "active" && info.Swarm.ControlAvailable && info.Swarm.Cluster.ID != "" {
				found("swarm:"+info.Swarm.Cluster.ID, name, agentproto.DeclaredTarget{
					Platform: agentproto.Swarm, Name: name, Swarm: &agentproto.SwarmSettings{DockerHost: host},
				})
			} else {
				found("docker:"+info.ID, name, agentproto.DeclaredTarget{
					Platform: agentproto.Docker, Name: name, Docker: &agentproto.DockerSettings{DockerHost: host},
				})
			}
		} else {
			r.Notes = append(r.Notes, "docker: "+err.Error())
		}
	}

	// A Linux host: its root mounted read-only at GOLIASH_HOST_ROOT (/host in the agent's container), or
	// "/" for an agent installed on the host itself.
	if root := o.Env("GOLIASH_HOST_ROOT"); root != "" {
		if o.Exists(path.Join(root, "etc", "os-release")) {
			name := ""
			if b, err := o.ReadFile(path.Join(root, "etc", "hostname")); err == nil {
				name = strings.TrimSpace(string(b))
			}
			if name == "" {
				name, _ = o.Hostname()
			}
			name = nameOr(name)
			id := name
			if b, err := o.ReadFile(path.Join(root, "etc", "machine-id")); err == nil && strings.TrimSpace(string(b)) != "" {
				id = strings.TrimSpace(string(b))
			}
			hostRoot := root
			found("host:"+id, name, agentproto.DeclaredTarget{
				Platform: agentproto.Host, Name: name, Host: &agentproto.HostSettings{Root: &hostRoot},
			})
		} else {
			r.Notes = append(r.Notes, "host: no etc/os-release under "+root)
		}
	}

	if raw := o.Env("GOLIASH_TARGETS"); raw != "" {
		var declared []agentproto.DeclaredTarget
		if err := json.Unmarshal([]byte(raw), &declared); err != nil {
			return r, fmt.Errorf("GOLIASH_TARGETS: %w", err)
		}
		for _, t := range declared {
			if !t.Platform.Valid() || t.Name == "" {
				return r, fmt.Errorf("GOLIASH_TARGETS: every target needs a known platform and a name")
			}
		}
		r.Targets = append(r.Targets, declared...)
	}

	if id := o.Env("GOLIASH_AGENT_ID"); id != "" {
		r.Identity = id
	}
	if r.Name == "" {
		h, _ := o.Hostname()
		r.Name = nameOr(h)
	}
	return r, nil
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

type info struct {
	ID    string
	Name  string
	Swarm struct {
		LocalNodeState   string
		ControlAvailable bool
		Cluster          struct{ ID string }
	}
}

func dockerInfo(ctx context.Context, host string) (info, error) {
	var in info
	c, err := docker.NewClient(host)
	if err != nil {
		return in, err
	}
	// The socket proxy may start after the agent.
	for i := 0; ; i++ {
		err = c.Get(ctx, "/info", &in)
		if err == nil || i == 4 || strings.Contains(err.Error(), ": 403") {
			break
		}
		select {
		case <-ctx.Done():
			return in, ctx.Err()
		case <-time.After(time.Duration(i+1) * time.Second):
		}
	}
	if err != nil {
		if strings.Contains(err.Error(), ": 403") {
			return in, fmt.Errorf("the socket proxy forbids /info: allow INFO=1 so the agent can tell this engine apart")
		}
		return in, err
	}
	if in.ID == "" {
		return in, errors.New("/info has no engine ID")
	}
	return in, nil
}

func ecsCluster(ctx context.Context, hc *http.Client, uri string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(uri, "/")+"/task", nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("task metadata: %d", resp.StatusCode)
	}
	var task struct{ Cluster string }
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return "", fmt.Errorf("task metadata: %w", err)
	}
	if !strings.HasPrefix(task.Cluster, "arn:") {
		return "", fmt.Errorf("task metadata names no cluster ARN (%q)", task.Cluster)
	}
	return task.Cluster, nil
}

// arnParts returns the region and the cluster name of an ECS cluster ARN
// (arn:aws:ecs:eu-west-1:123456789012:cluster/prod).
func arnParts(arn string) (region, cluster string) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) == 6 {
		region = parts[3]
		cluster = strings.TrimPrefix(parts[5], "cluster/")
	}
	return region, cluster
}

// discoverAWS adds, for the agent's region and those in GOLIASH_AWS_REGIONS, every
// ECS cluster (or those in GOLIASH_ECS_CLUSTERS, without those in GOLIASH_ECS_EXCLUDE)
// as a target of its own, so each can
// sit in its own environment, and the region's Lambda functions (GOLIASH_LAMBDA=off
// leaves them out). Platforms the credentials may not read are left out with a note.
func (o Options) discoverAWS(ctx context.Context, r *Result, found func(string, string, agentproto.DeclaredTarget),
	nameOr func(string) string, home, ownCluster string,
) {
	account, err := o.AWS.Account(ctx, home)
	if err != nil {
		r.Notes = append(r.Notes, "aws: "+err.Error())
		return
	}
	identity, agentName := "aws:"+account+"/"+home, nameOr("aws-"+home)
	if ownCluster != "" {
		_, cluster := arnParts(ownCluster)
		identity, agentName = "ecs:"+ownCluster, nameOr(cluster)
	}
	regions := []string{home}
	for _, rg := range strings.Split(o.Env("GOLIASH_AWS_REGIONS"), ",") {
		if rg = strings.TrimSpace(rg); rg != "" && !slices.Contains(regions, rg) {
			regions = append(regions, rg)
		}
	}
	only, skip := map[string]bool{}, map[string]bool{}
	for _, c := range strings.Split(o.Env("GOLIASH_ECS_CLUSTERS"), ",") {
		if c = strings.TrimSpace(c); c != "" {
			only[c] = true
		}
	}
	for _, c := range strings.Split(o.Env("GOLIASH_ECS_EXCLUDE"), ",") {
		if c = strings.TrimSpace(c); c != "" {
			skip[c] = true
		}
	}
	// Target names: the cluster in the agent's region, with the region elsewhere, so
	// clusters of the same name in two regions stay apart.
	named := func(name, region string) string {
		if region == home {
			return name
		}
		return name + "-" + region
	}
	for _, region := range regions {
		arns, err := o.AWS.ECSClusters(ctx, region)
		switch {
		case err != nil && ownCluster != "" && region == home:
			// Without ecs:ListClusters the agent still knows its own cluster.
			arns = []string{ownCluster}
		case err != nil:
			r.Notes = append(r.Notes, "ecs "+region+": "+err.Error())
		}
		for _, arn := range arns {
			_, cluster := arnParts(arn)
			if (len(only) > 0 && !only[cluster]) || skip[cluster] {
				continue
			}
			found(identity, agentName, agentproto.DeclaredTarget{
				Platform: agentproto.Ecs, Name: named(cluster, region),
				Ecs: &agentproto.ECSSettings{Region: region, Clusters: []string{cluster}},
			})
		}
		if o.Env("GOLIASH_LAMBDA") == "off" {
			continue
		}
		if err := o.AWS.CanListFunctions(ctx, region); err == nil {
			found(identity, agentName, agentproto.DeclaredTarget{
				Platform: agentproto.Lambda, Name: "lambda-" + region,
				Lambda: &agentproto.LambdaSettings{Region: region, AliasEnvironments: aliasEnvironments(o.Env("GOLIASH_LAMBDA_ALIASES"))},
			})
		} else {
			r.Notes = append(r.Notes, "lambda "+region+": "+err.Error())
		}
	}
}

// sdkAWS is AWS through the SDK and the default credential chain.
type sdkAWS struct{}

func awsConfig(ctx context.Context, region string) (aws.Config, error) {
	return awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
}

func (sdkAWS) Account(ctx context.Context, region string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cfg, err := awsConfig(ctx, region)
	if err != nil {
		return "", err
	}
	who, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", err
	}
	return aws.ToString(who.Account), nil
}

func (sdkAWS) ECSClusters(ctx context.Context, region string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cfg, err := awsConfig(ctx, region)
	if err != nil {
		return nil, err
	}
	var arns []string
	p := ecs.NewListClustersPaginator(ecs.NewFromConfig(cfg), &ecs.ListClustersInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		arns = append(arns, page.ClusterArns...)
	}
	return arns, nil
}

func (sdkAWS) CanListFunctions(ctx context.Context, region string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cfg, err := awsConfig(ctx, region)
	if err != nil {
		return err
	}
	one := int32(1)
	_, err = lambda.NewFromConfig(cfg).ListFunctions(ctx, &lambda.ListFunctionsInput{MaxItems: &one})
	return err
}

// aliasEnvironments reads GOLIASH_LAMBDA_ALIASES: "live=prod,canary=-" maps Lambda
// aliases to environments named otherwise, "-" leaves an alias out.
func aliasEnvironments(v string) map[string]string {
	var out map[string]string
	for _, pair := range strings.Split(v, ",") {
		alias, env, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || strings.TrimSpace(alias) == "" || strings.TrimSpace(env) == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[strings.TrimSpace(alias)] = strings.TrimSpace(env)
	}
	return out
}
