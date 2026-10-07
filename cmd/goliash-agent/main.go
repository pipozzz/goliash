// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Command goliash-agent runs inside the customer network, reads orchestrators
// read-only and sends snapshots to a Goliash server over outbound HTTPS.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pipozzz/goliash/internal/agent"
	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/internal/collectors/compose"
	"github.com/pipozzz/goliash/internal/collectors/docker"
	"github.com/pipozzz/goliash/internal/collectors/ecs"
	"github.com/pipozzz/goliash/internal/collectors/kubernetes"
	"github.com/pipozzz/goliash/internal/collectors/lambda"
	"github.com/pipozzz/goliash/internal/collectors/nomad"
	"github.com/pipozzz/goliash/internal/collectors/swarm"
	"github.com/pipozzz/goliash/internal/discover"
	"github.com/pipozzz/goliash/pkg/agentproto"
	"github.com/pipozzz/goliash/pkg/buildinfo"
)

func main() {
	server := flag.String("server", os.Getenv("GOLIASH_SERVER_URL"), "Goliash server URL (env GOLIASH_SERVER_URL)")
	dataDir := flag.String("data-dir", envOr("GOLIASH_DATA_DIR", "data"), "directory for buffered snapshots (env GOLIASH_DATA_DIR)")
	debug := flag.Bool("debug", os.Getenv("GOLIASH_DEBUG") != "", "debug logging (env GOLIASH_DEBUG)")
	showVersion := flag.Bool("version", false, "print version and exit")
	logFormat := flag.String("log-format", envOr("GOLIASH_LOG_FORMAT", "text"), "text or json (env GOLIASH_LOG_FORMAT)")
	flag.Parse()

	if *showVersion {
		fmt.Println("goliash-agent", buildinfo.String())
		return
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	var log *slog.Logger
	switch *logFormat {
	case "json":
		log = slog.New(slog.NewJSONHandler(os.Stderr, opts))
	case "text", "":
		log = slog.New(slog.NewTextHandler(os.Stderr, opts))
	default:
		fmt.Fprintf(os.Stderr, "log format %q: use text or json\n", *logFormat)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	code, token, err := credential()
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	log.Info("goliash-agent starting", "version", buildinfo.Version, "server", *server)
	reenrolled := 0
	for {
		if code != "" {
			if token, err = enroll(ctx, *server, code, log); err != nil {
				if ctx.Err() != nil {
					break
				}
				log.Error("cannot enroll", "err", err)
				os.Exit(1)
			}
		}
		a, err := agent.New(agent.Options{
			ServerURL: *server,
			Token:     token,
			DataDir:   *dataDir,
			Collectors: map[agentproto.Platform]collectors.Factory{
				agentproto.Kubernetes: kubernetes.New,
				agentproto.Ecs:        ecs.New,
				agentproto.Lambda:     lambda.New,
				agentproto.Nomad:      nomad.New,
				agentproto.Swarm:      swarm.New,
				agentproto.Docker:     docker.New,
				agentproto.Compose:    compose.New,
			},
			Logger: log,
		})
		if err != nil {
			log.Error("invalid configuration", "err", err)
			os.Exit(2)
		}
		err = a.Run(ctx)
		if code != "" && agent.Unauthorized(err) {
			// Another start of this installation enrolled, or the token was revoked:
			// enrolling again tells which.
			// Two agents with one identity take the agent from each other: wait longer each time.
			reenrolled++
			wait := min(time.Duration(reenrolled)*5*time.Second, 5*time.Minute)
			log.Warn("token rejected; enrolling again (if this repeats, a second agent runs with the same identity: give one GOLIASH_AGENT_ID)",
				"err", err, "retry_in", wait)
			select {
			case <-ctx.Done():
			case <-time.After(wait):
				continue
			}
		}
		if err != nil && ctx.Err() == nil {
			log.Error("goliash-agent stopped", "err", err)
			os.Exit(1)
		}
		break
	}
	log.Info("goliash-agent stopped")
}

// credential returns the agent's enrollment code or its token. The code comes from
// GOLIASH_ENROLL_CODE, or from wherever a token goes (GOLIASH_AGENT_TOKEN or its
// file), so existing deployments take a code in place of a token. An agent with a
// code needs no token: it enrolls on every start.
func credential() (code, token string, err error) {
	if c := cleanToken(os.Getenv("GOLIASH_ENROLL_CODE")); c != "" {
		if !strings.HasPrefix(c, "glsh_enroll_") {
			return "", "", errors.New("GOLIASH_ENROLL_CODE does not start with glsh_enroll_; copy the code from Connect an agent in Goliash")
		}
		return c, "", nil
	}
	raw, source, err := rawToken()
	if err != nil {
		return "", "", err
	}
	if strings.HasPrefix(raw, "glsh_enroll_") {
		return raw, "", nil
	}
	token, err = checkToken(raw, source)
	return "", token, err
}

// enroll finds what this agent can collect and exchanges the code for a token.
func enroll(ctx context.Context, server, code string, log *slog.Logger) (string, error) {
	found, err := discover.Run(ctx, discover.Options{})
	for _, n := range found.Notes {
		log.Warn("platform not usable", "detail", n)
	}
	if err != nil {
		return "", err
	}
	host, _ := os.Hostname()
	resp, err := agent.Enroll(ctx, server, agentproto.EnrollRequest{
		Code: code, Identity: found.Identity, Name: found.Name, Version: buildinfo.Version, Hostname: host,
		Targets: found.Targets, Notes: shortNotes(found.Notes),
	}, nil, log)
	if err != nil {
		return "", err
	}
	names := make([]string, len(found.Targets))
	for i, t := range found.Targets {
		names[i] = string(t.Platform) + " " + t.Name
	}
	log.Info("enrolled", "agent", resp.Name, "agent_id", resp.AgentID, "targets", strings.Join(names, ", "))
	return resp.Token, nil
}

// rawToken reads GOLIASH_AGENT_TOKEN, or the file named by GOLIASH_AGENT_TOKEN_FILE
// (Docker and Kubernetes secrets), and says where it came from.
func rawToken() (token, source string, err error) {
	if t := os.Getenv("GOLIASH_AGENT_TOKEN"); t != "" {
		return cleanToken(t), "GOLIASH_AGENT_TOKEN", nil
	}
	path := os.Getenv("GOLIASH_AGENT_TOKEN_FILE")
	if path == "" {
		return "", "", errors.New("set GOLIASH_ENROLL_CODE (from Connect an agent in Goliash) or GOLIASH_AGENT_TOKEN")
	}
	b, err := os.ReadFile(path) //nolint:gosec // path comes from the operator
	if err != nil {
		return "", "", fmt.Errorf("agent token file: %w", err)
	}
	return cleanToken(string(b)), path, nil
}

// cleanToken drops what copying and configuration files add around a token:
// whitespace, line breaks and quotes.
func cleanToken(t string) string {
	t = strings.TrimSpace(t)
	if len(t) >= 2 && (t[0] == '"' || t[0] == '\'') && t[len(t)-1] == t[0] {
		t = strings.TrimSpace(t[1 : len(t)-1])
	}
	return t
}

// agentTokenLen is the length of glsh_agent_ plus 30 random and 6 checksum characters.
const agentTokenLen = len("glsh_agent_") + 36

// checkToken explains a token that the server would refuse because of its shape,
// without printing it.
func checkToken(t, source string) (string, error) {
	switch {
	case strings.HasPrefix(t, "glsh_api_"), strings.HasPrefix(t, "glsh_ci_"):
		return "", fmt.Errorf("%s holds an API or CI token; the agent needs its own token, glsh_agent_…, shown when the agent is created", source)
	case !strings.HasPrefix(t, "glsh_agent_"):
		return "", fmt.Errorf("%s does not start with glsh_agent_ or glsh_enroll_; copy the code from Connect an agent in Goliash", source)
	case strings.ContainsAny(t, " \t\r\n\"'"):
		return "", fmt.Errorf("%s contains spaces or quotes inside the token; copy it again", source)
	case len(t) != agentTokenLen:
		return "", fmt.Errorf("%s is %d characters long, an agent token has %d: it was cut or joined when copied", source, len(t), agentTokenLen)
	}
	return t, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// shortNotes keeps what the agent reports it could not use within the protocol's
// limits: 50 notes of 500 bytes.
func shortNotes(notes []string) []string {
	out := make([]string, 0, min(len(notes), 50))
	for _, n := range notes[:min(len(notes), 50)] {
		if len(n) > 500 {
			n = strings.ToValidUTF8(n[:497], "") + "…"
		}
		out = append(out, n)
	}
	return out
}
